package main

import (
	"context"

	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/charmbracelet/x/term"
	"github.com/coder/websocket"
)

// cmdSSH is the dashboard's terminal, in your own terminal. It carries the same
// pty stream the browser uses, so it lands in a shell the same way, with no
// second authentication in between.
//
// It is not the ssh protocol and cannot carry scp or rsync. For those, forward
// the target's own sshd instead: `beacon forward AGENT ssh`.
func cmdSSH(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: beacon ssh AGENT   (for example: beacon ssh mm01ops)")
	}
	agent := args[0]

	fs := flag.NewFlagSet("beacon ssh", flag.ExitOnError)
	fs.String(keyServer, "", "relay URL, http:// or https://")
	fs.String(keyCA, "", "PEM bundle trusted in addition to the system roots")
	fs.Usage = func() {
		usageFor(fs, "beacon ssh AGENT", "Open a terminal on a machine, in this terminal.")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if !term.IsTerminal(os.Stdin.Fd()) {
		return fmt.Errorf("this needs a terminal; to script a command use the MCP endpoint instead")
	}

	cfg, err := loadConfig(explicitFlags(fs))
	if err != nil {
		return err
	}
	tlsCfg, err := tlsConfig(cfg.CAFile)
	if err != nil {
		return err
	}
	target, err := shellURL(cfg.Server, agent)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	c, resp, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		if resp != nil {
			return fmt.Errorf("relay refused the session: %s", resp.Status)
		}
		return err
	}
	defer c.CloseNow()

	conn := websocket.NetConn(ctx, c, websocket.MessageBinary)
	w := &frameWriter{w: conn}

	// From here the terminal is raw, so every failure has to restore it before
	// returning; a half-restored terminal leaves the shell unusable.
	state, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return err
	}
	defer term.Restore(os.Stdin.Fd(), state)

	w.resize()
	stopWatching := watchResize(w.resize)
	defer stopWatching()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if werr := w.data(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// the agent answers with raw terminal output, so this end parses nothing
	_, err = io.Copy(os.Stdout, conn)
	term.Restore(os.Stdin.Fd(), state)
	fmt.Println()
	return err
}

// frameWriter serialises the two things this end sends. Keystrokes and window
// sizes share one connection, and a frame is a header followed by a payload:
// interleaving two of them would corrupt both.
type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (f *frameWriter) data(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return protocol.WritePTYFrame(f.w, protocol.PTYData, p)
}

func (f *frameWriter) resize() {
	cols, rows, err := term.GetSize(os.Stdout.Fd())
	if err != nil || cols <= 0 || rows <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	protocol.WritePTYFrame(f.w, protocol.PTYResize, protocol.PTYResizePayload(uint16(cols), uint16(rows)))
}

func shellURL(server, agent string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported scheme %q, expected http or https", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("server URL %q has no host", server)
	}
	u.Path = "/api/agents/" + url.PathEscape(agent) + "/shell"
	u.RawQuery = ""
	return u.String(), nil
}
