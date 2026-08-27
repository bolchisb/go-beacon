package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/bolchisb/go-beacon/internal/supervise"
	"github.com/coder/websocket"
)

// cmdForward opens a local port that leads to a service on a remote machine.
//
// The port has to be here, next to the desktop client: a remote desktop client
// speaks TCP and knows nothing about the relay, and the relay itself answers on
// 443 and nothing else. So the tunnel is a WebSocket to the relay, and this
// end of it looks like an ordinary server on localhost.
//
// --stdio drops the port. ssh can spawn its own transport through
// ProxyCommand, and a command that already speaks on stdin and stdout needs no
// address to be agreed on beforehand -- which is what lets one ~/.ssh/config
// entry cover every agent.
func cmdForward(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: beacon forward AGENT SERVICE [flags]   (for example: beacon forward target-01 rdp)")
	}
	agent, service := args[0], args[1]

	fs := flag.NewFlagSet("beacon forward", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:0", "local address to listen on; port 0 picks a free one")
	stdio := fs.Bool("stdio", false, "carry one session on stdin and stdout instead of a local port, for ssh ProxyCommand")
	fs.String(keyServer, "", "relay URL, http:// or https://")
	fs.String(keyCA, "", "PEM bundle trusted in addition to the system roots")
	fs.String(keyToken, "", "operator token for the relay's API")
	fs.Usage = func() {
		usageFor(fs, "beacon forward AGENT SERVICE", "Open a local port that leads to a service on a remote machine.")
	}
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}

	cfg, err := loadConfig(explicitFlags(fs))
	if err != nil {
		return err
	}
	tlsCfg, err := tlsConfig(cfg.CAFile)
	if err != nil {
		return err
	}
	target, err := forwardURL(cfg.Server, agent, service)
	if err != nil {
		return err
	}

	if *stdio {
		// stdout is the tunnel from here on. Anything else written there is
		// read by ssh as protocol and kills the session, so the logger -- set
		// to stdout for every other command -- moves aside, and the panel that
		// would announce the port is not drawn at all.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		bridge(ctx, stdioConn{}, target, tlsCfg, cfg.Token, cfg.Session)
		return nil
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer ln.Close()

	p := resultPanel("forward", markLive, styOK, "LISTENING", "")
	p.kv("agent", agent)
	p.kv("service", service)
	p.kv("connect", ln.Addr().String())
	p.kv("relay", cfg.Server)
	p.footer = "ctrl-c to stop"
	p.show()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	supervise.Go("forward-shutdown", func() {
		<-ctx.Done()
		ln.Close()
	})

	for {
		local, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				fmt.Println(styDim.Render("  stopped"))
				return nil
			}
			return err
		}
		supervise.Go("forward-session", func() { bridge(ctx, local, target, tlsCfg, cfg.Token, cfg.Session) })
	}
}

// bridge carries one accepted connection over its own WebSocket. One
// connection per session keeps a failure local to the session that caused it.
func bridge(ctx context.Context, local io.ReadWriteCloser, target string, tlsCfg *tls.Config, token, session string) {
	defer local.Close()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	c, resp, err := websocket.Dial(ctx, target, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: apiHeader(token, session),
	})
	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusUnauthorized {
				slog.Warn("forward: not signed in: run `beacon login`")
			} else {
				slog.Warn("forward: relay refused the session", "status", resp.Status)
			}
		} else {
			slog.Warn("forward: cannot reach the relay", "err", err)
		}
		return
	}
	defer c.CloseNow()

	remote := websocket.NetConn(ctx, c, websocket.MessageBinary)

	// the agent answers before any traffic flows, so a refusal arrives as a
	// reason rather than as a session that opened and closed
	br := bufio.NewReader(remote)
	if err := protocol.ReadForwardStatus(br); err != nil {
		slog.Warn("forward: the agent refused", "err", err)
		return
	}
	remote = protocol.WithBuffered(remote, br)

	from := "stdio"
	if c, ok := local.(net.Conn); ok {
		from = c.RemoteAddr().String()
	}
	slog.Info("forward: session open", "from", from)

	var once sync.Once
	shut := func() { once.Do(func() { local.Close(); c.CloseNow() }) }
	defer shut()

	supervise.Go("forward-upstream", func() {
		io.Copy(remote, local)
		// The client may only be done sending. Half-closing here lets the far
		// side finish replying; tearing the session down would drop whatever
		// was already on its way back.
		if tcp, ok := local.(*net.TCPConn); ok {
			tcp.CloseRead()
		}
	})

	// ends when the far side closes, or when the client is gone and writing
	// to it fails
	io.Copy(local, remote)
	slog.Info("forward: session closed")
}

// stdioConn presents this process's standard streams as the same
// io.ReadWriteCloser the listener hands to bridge, so --stdio is one more
// source of a single connection rather than a second copy of the pump.
type stdioConn struct{}

func (stdioConn) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdioConn) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

// Closing stdin is what unblocks the read half; stdout belongs to the parent
// process and is left alone.
func (stdioConn) Close() error { return os.Stdin.Close() }

// forwardURL turns the relay's http(s) address into the ws(s) endpoint for one
// service on one agent.
func forwardURL(server, agent, service string) (string, error) {
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
	u.Path = "/api/agents/" + url.PathEscape(agent) + "/forward/" + url.PathEscape(service)
	u.RawQuery = ""
	return u.String(), nil
}
