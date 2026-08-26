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
func cmdForward(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: beacon forward AGENT SERVICE [flags]   (for example: beacon forward mm01ops rdp)")
	}
	agent, service := args[0], args[1]

	fs := flag.NewFlagSet("beacon forward", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:0", "local address to listen on; port 0 picks a free one")
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
		supervise.Go("forward-session", func() { bridge(ctx, local, target, tlsCfg, cfg.Token) })
	}
}

// bridge carries one accepted connection over its own WebSocket. One
// connection per session keeps a failure local to the session that caused it.
func bridge(ctx context.Context, local net.Conn, target string, tlsCfg *tls.Config, token string) {
	defer local.Close()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	c, resp, err := websocket.Dial(ctx, target, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: apiHeader(token),
	})
	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusUnauthorized {
				slog.Warn("forward: the relay wants an operator token; " +
					"set one with `beacon config set token=...`, or pass --token")
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

	slog.Info("forward: session open", "from", local.RemoteAddr().String())

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
