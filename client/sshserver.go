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
	"strings"
	"sync"

	gssh "github.com/gliderlabs/ssh"
	"github.com/bolchisb/go-beacon/internal/supervise"
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
	sshMu     sync.Mutex
	sshServer *gssh.Server
)

// resetSSHServer exists for tests, which need a fresh server per case.
func resetSSHServer() {
	sshMu.Lock()
	defer sshMu.Unlock()
	sshServer = nil
}

// embeddedSSH builds the server once and reuses it. Each stream is a separate
// connection to the same server.
//
// A failure is deliberately not remembered. Caching it would have been cheaper
// and would have meant that one transient fault -- a disk briefly full while
// the host key is written -- left this service dead until somebody restarted
// an agent on a machine reachable only through the tunnel that agent serves.
// The next connection builds it again.
func embeddedSSH(cfg *Config) (*gssh.Server, error) {
	sshMu.Lock()
	defer sshMu.Unlock()
	if sshServer != nil {
		return sshServer, nil
	}

	signer, err := hostKey(cfg)
	if err != nil {
		return nil, err
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
			// What an editor uses to reach a dev server running on the
			// target: the Ports panel, and every "open in browser" that
			// follows from it. Without it the editor connects and the panel
			// silently forwards nothing.
			"direct-tcpip": gssh.DirectTCPIPHandler,
		},
		// Loopback only. A forward that could name any address would make
		// this agent the route into the customer's network that every other
		// part of the design refuses to be -- the same reason a forward
		// stream names a service rather than a host and port. The target's
		// own localhost is where a dev server lives, and it is enough.
		LocalPortForwardingCallback: func(_ gssh.Context, host string, port uint32) bool {
			if isLoopbackHost(host) {
				return true
			}
			slog.Warn("ssh: refused a forward off loopback", "host", host, "port", port)
			return false
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
	return srv, nil
}

// isLoopbackHost reports whether a forward destination stays on the target.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// sessionHandler runs one ssh session. It is the outermost frame of a
// goroutine this process did not start: gliderlabs runs each session and each
// subsystem in one of its own, with no recover in between, so a panic here
// would take the agent down and with it the only way back onto the machine.
func sessionHandler(s gssh.Session) {
	defer supervise.Recover("ssh-session")

	// The ssh username selects the environment. Authentication accepts any
	// credential -- the tunnel is the gate -- so the name is free to carry a
	// choice instead, and `ssh wsl@host` is a good deal easier to remember than
	// a flag nobody has in their editor's connection settings.
	if s.User() == "wsl" {
		serveWSL(s)
		return
	}

	name, args := shellFor(s.RawCommand())
	serveSession(s, name, args)
}

// serveSession runs one program for a session, with a terminal or without, and
// reports what it exited with. Both the machine's own shell and a shell inside
// WSL come through here; they differ only in what they run.
func serveSession(s gssh.Session, name string, args []string) {
	ptyReq, winCh, isPty := s.Pty()

	if !isPty {
		// No terminal: scp, git and rsync all land here. Running without a pty
		// matters -- a pty would corrupt their protocols with echo and line
		// editing.
		runWithoutPTY(s, name, args)
		return
	}

	env := s.Environ()
	if ptyReq.Term != "" {
		env = append(env, "TERM="+ptyReq.Term)
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
	supervise.Go("ssh-window", func() {
		for w := range winCh {
			_ = term.Resize(uint16(w.Width), uint16(w.Height))
		}
	})
	supervise.Go("ssh-stdin", func() { _, _ = io.Copy(term, s) })

	// Output is copied on this goroutine rather than raced against the input
	// half. Returning on whichever finished first cut the session off while
	// the program was still writing, which a client that closes its own stdin
	// -- `ssh host cmd < /dev/null`, and every editor that does the same --
	// hit every time. This returns when the terminal closes, which is after
	// the program exited and everything it wrote has been read.
	_, _ = io.Copy(s, term)

	// Closing before waiting matters when it was the client that vanished: the
	// program is still running and nothing else would ever reap it.
	_ = term.Close()
	_ = s.Exit(term.Wait())
}
// sftpHandler serves the subsystem VS Code Remote-SSH and scp rely on. Without
// it an editor connects, reports success, and then cannot open a single file.
func sftpHandler(s gssh.Session) {
	defer supervise.Recover("ssh-sftp")

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
//
// The command must be the raw one. Session.Command() hands back an argv the
// library already split with shlex, and re-joining that on spaces loses every
// quote the client wrote: rsync and scp send paths as one quoted argument, so
// a directory with a space in its name arrived at the shell as two.
func shellFor(command string) (string, []string) {
	if strings.TrimSpace(command) != "" {
		return commandShell(command)
	}
	return interactiveShell()
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
		persistHostKey(cfg.SSHHostKey)
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
	if err == nil {
		return 0
	}
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
	// A command has to be carried into the distribution. Without this the
	// default shell opened instead and the command was dropped, which scp and
	// rsync read as a session that never answers.
	if cmd := strings.TrimSpace(s.RawCommand()); cmd != "" {
		args = append(args, "--", "sh", "-c", cmd)
	}
	serveSession(s, name, args)
}
