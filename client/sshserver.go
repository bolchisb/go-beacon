package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"sync"

	gssh "github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	xssh "golang.org/x/crypto/ssh"
)

// A real SSH server, embedded in the agent.
//
// The terminal the relay already offered is a raw pty, which is enough for a
// person typing but not for the tools people actually develop with: VS Code
// Remote-SSH, JetBrains Gateway, scp, rsync and git all speak the SSH protocol
// and want an SFTP subsystem. This is that server, so none of it depends on the
// target machine having sshd installed -- which on Windows it usually does not.
//
// It never binds a port. Connections arrive as yamux streams that already came
// through the relay's authenticated tunnel, and are handed to HandleConn. A
// listener on loopback would have handed a shell to every local user on the
// machine, which is the one thing this must not do.

var (
	sshOnce   sync.Once
	sshServer *gssh.Server
	sshErr    error
)

// resetSSHServer exists for tests, which need a fresh server per case. The
// sync.Once is right in production, where one server serves every stream.
func resetSSHServer() {
	sshOnce = sync.Once{}
	sshServer, sshErr = nil, nil
}

// embeddedSSH builds the server once and reuses it. Each stream is a separate
// connection to the same server.
func embeddedSSH(cfg *Config) (*gssh.Server, error) {
	sshOnce.Do(func() {
		signer, err := hostKey(cfg)
		if err != nil {
			sshErr = err
			return
		}
		srv := &gssh.Server{
			Handler: sessionHandler,
			// Declared explicitly because this server is driven by HandleConn
			// rather than Serve, and the defaults are installed by the latter.
			// Without them the handshake succeeds and every session channel is
			// rejected as an unknown type -- a connection that looks fine and
			// can do nothing.
			ChannelHandlers: map[string]gssh.ChannelHandler{
				"session": gssh.DefaultSessionHandler,
			},
			SubsystemHandlers: map[string]gssh.SubsystemHandler{
				"sftp": sftpHandler,
			},
			// Every credential is accepted, and that is deliberate rather than
			// an omission. The gate is the tunnel: a stream only reaches here
			// after the relay authenticated an operator and the agent proved
			// its own identity. Asking again for a credential this machine
			// would have to store, and that no operator has, would add a
			// secret without adding a check.
			PublicKeyHandler:           func(gssh.Context, gssh.PublicKey) bool { return true },
			PasswordHandler:            func(gssh.Context, string) bool { return true },
			KeyboardInteractiveHandler: func(gssh.Context, xssh.KeyboardInteractiveChallenge) bool { return true },
		}
		srv.AddHostKey(signer)
		sshServer = srv
	})
	return sshServer, sshErr
}

// handleSSH serves one relay stream as an SSH connection.
func handleSSH(stream net.Conn, cfg *Config) {
	srv, err := embeddedSSH(cfg)
	if err != nil {
		slog.Error("ssh: cannot start the embedded server", "err", err)
		stream.Close()
		return
	}
	// HandleConn takes ownership of the connection and closes it.
	srv.HandleConn(stream)
}

// sessionHandler runs one ssh session: a terminal when the client asked for
// one, otherwise the command it sent.
func sessionHandler(s gssh.Session) {
	ptyReq, winCh, isPty := s.Pty()

	// The ssh username selects the environment. Authentication accepts any
	// credential -- the tunnel is the gate -- so the name is free to carry a
	// choice instead, and `ssh wsl@host` is a good deal easier to remember than
	// a flag nobody has in their editor's connection settings.
	if s.User() == "wsl" {
		serveWSL(s)
		return
	}

	name, args := shellFor(s.Command())
	env := s.Environ()
	if isPty && ptyReq.Term != "" {
		env = append(env, "TERM="+ptyReq.Term)
	}

	if !isPty {
		// No terminal: scp, git and rsync all land here. Running without a pty
		// matters -- a pty would corrupt their protocols with echo and line
		// editing.
		runWithoutPTY(s, name, args)
		return
	}

	term, err := startPTYWith(name, args, env)
	if err != nil {
		fmt.Fprintf(s, "cannot open a terminal: %v\r\n", err)
		_ = s.Exit(1)
		return
	}
	defer term.Close()

	if ptyReq.Window.Width > 0 {
		_ = term.Resize(uint16(ptyReq.Window.Width), uint16(ptyReq.Window.Height))
	}
	go func() {
		for w := range winCh {
			_ = term.Resize(uint16(w.Width), uint16(w.Height))
		}
	}()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(term, s); done <- struct{}{} }()
	go func() { _, _ = io.Copy(s, term); done <- struct{}{} }()
	<-done
	_ = s.Exit(0)
}

// sftpHandler serves the subsystem VS Code Remote-SSH and scp rely on. Without
// it an editor connects, reports success, and then cannot open a single file.
func sftpHandler(s gssh.Session) {
	server, err := sftp.NewServer(s)
	if err != nil {
		slog.Warn("sftp: cannot start", "err", err)
		return
	}
	defer server.Close()
	if err := server.Serve(); err != nil && err != io.EOF {
		slog.Debug("sftp: session ended", "err", err)
	}
}

// shellFor picks what to run: the client's command if it sent one, otherwise
// an interactive shell. The per-platform halves live in sshshell_*.go, because
// deciding at runtime would still need the Windows shell lookup to compile on
// unix.
func shellFor(command []string) (string, []string) {
	if len(command) > 0 {
		return commandShell(joinCommand(command))
	}
	return interactiveShell()
}

func joinCommand(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}

// hostKey returns this machine's ssh host key, generating one the first time.
// Stable across restarts so a client does not warn about a changed key on every
// reconnect.
func hostKey(cfg *Config) (xssh.Signer, error) {
	if cfg.SSHHostKey == "" {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		cfg.SSHHostKey = base64.StdEncoding.EncodeToString(priv)
		if _, err := saveConfig(*cfg); err != nil {
			// Not fatal: the session works, the client just sees a new host key
			// next time. Saying so beats failing the connection.
			slog.Warn("ssh: could not persist the host key", "err", err)
		}
	}
	raw, err := base64.StdEncoding.DecodeString(cfg.SSHHostKey)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("the stored ssh host key is unusable")
	}
	return xssh.NewSignerFromKey(ed25519.PrivateKey(raw))
}

// runWithoutPTY handles a session with no terminal: a plain command, or a
// subsystem-less transfer like scp.
func runWithoutPTY(s gssh.Session, name string, args []string) {
	cmd := execCommand(s.Context(), name, args...)
	cmd.Stdin = s
	cmd.Stdout = s
	cmd.Stderr = s.Stderr()
	if err := cmd.Run(); err != nil {
		_ = s.Exit(exitCodeOf(err))
		return
	}
	_ = s.Exit(0)
}

func execCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// exitCodeOf reports what the command exited with, so the ssh client sees the
// real status rather than a blanket failure.
func exitCodeOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// serveEmbeddedSSH is the entry point from a forward stream. The config is
// re-read rather than captured: the host key may have been written on a
// previous connection, and a stale copy would regenerate it.
func serveEmbeddedSSH(stream net.Conn) {
	r, err := loadConfig(nil)
	if err != nil {
		slog.Error("ssh: cannot read the config", "err", err)
		stream.Close()
		return
	}
	handleSSH(stream, &r.Config)
}

// serveWSL runs a shell inside WSL, when this agent can reach one.
//
// Refusing with the reason matters more than usual here: the common failure is
// not "WSL is missing" but "this agent runs as a service and WSL belongs to a
// user", and those want completely different fixes.
func serveWSL(s gssh.Session) {
	state := detectWSL()
	if !state.usable() {
		fmt.Fprintf(s, "WSL is not available through this agent: %s\r\n", state.Detail)
		if state.Status == "unreachable" {
			fmt.Fprint(s, "Run the agent in a user session, or install the service "+
				"under the account that owns the distribution.\r\n")
		}
		_ = s.Exit(1)
		return
	}

	name, args := wslShell("")
	ptyReq, winCh, isPty := s.Pty()
	if !isPty {
		runWithoutPTY(s, name, args)
		return
	}

	term, err := startPTYWith(name, args, s.Environ())
	if err != nil {
		fmt.Fprintf(s, "cannot start WSL: %v\r\n", err)
		_ = s.Exit(1)
		return
	}
	defer term.Close()

	if ptyReq.Window.Width > 0 {
		_ = term.Resize(uint16(ptyReq.Window.Width), uint16(ptyReq.Window.Height))
	}
	go func() {
		for w := range winCh {
			_ = term.Resize(uint16(w.Width), uint16(w.Height))
		}
	}()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(term, s); done <- struct{}{} }()
	go func() { _, _ = io.Copy(s, term); done <- struct{}{} }()
	<-done
	_ = s.Exit(0)
}
