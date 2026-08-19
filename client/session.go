package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/hashicorp/yamux"
)

const (
	dialTimeout      = 10 * time.Second
	handshakeTimeout = 10 * time.Second
	pingInterval     = 5 * time.Second
)

// runSession holds one tunnel open until it dies or the process is stopped.
func runSession(ctx context.Context, server string, hello protocol.Hello, tlsCfg *tls.Config, st *agentState) error {
	conn, err := dialUpgrade(ctx, server, hello, tlsCfg)
	if err != nil {
		return err
	}
	counted := protocol.NewCountingConn(conn)

	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	// keepalive is left on: it is what notices a NAT that dropped the flow
	// without ever sending a FIN, which is the common failure on this path
	sess, err := yamux.Client(counted, ycfg)
	if err != nil {
		conn.Close()
		return err
	}
	defer sess.Close()

	// tear the session down on shutdown, and make sure this watcher exits with
	// the session rather than outliving it
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			sess.Close()
		case <-done:
		}
	}()

	st.connected(sess, counted)
	go pingLoop(st, sess)

	if logStream, err := sess.OpenStream(); err == nil {
		if protocol.WriteStreamHeader(logStream, protocol.StreamLog) == nil && shipper != nil {
			shipper.attach(logStream)
			defer func() { shipper.detach(logStream); logStream.Close() }()
		} else {
			logStream.Close()
		}
	}

	slog.Info("tunnel established", "server", server, "local", conn.LocalAddr().String())

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return err
		}
		go handleStream(stream)
	}
}

// pingLoop keeps a live round-trip figure for `beacon status`. It measures the
// tunnel, not the socket.
func pingLoop(st *agentState, sess *yamux.Session) {
	measure := func() bool {
		rtt, err := sess.Ping()
		if err != nil {
			return false
		}
		st.rttNanos.Store(int64(rtt))
		return true
	}
	if !measure() {
		return
	}

	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-sess.CloseChan():
			return
		case <-t.C:
			if !measure() {
				return
			}
		}
	}
}

// dialUpgrade performs the HTTP/1.1 upgrade by hand and returns the raw
// connection underneath it. Going out over HTTP rather than a bare TCP port is
// what lets the agent reach the relay from a locked-down network.
func dialUpgrade(ctx context.Context, server string, hello protocol.Hello, tlsCfg *tls.Config) (net.Conn, error) {
	u, err := url.Parse(server)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, fmt.Errorf("server URL %q has no host", server)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("unsupported scheme %q, expected http or https", u.Scheme)
	}
	u.Path = protocol.ConnectPath

	conn, err := dial(ctx, u, tlsCfg)
	if err != nil {
		return nil, err
	}

	tunnel, err := upgrade(conn, u, hello)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return tunnel, nil
}

func dial(ctx context.Context, u *url.URL, tlsCfg *tls.Config) (net.Conn, error) {
	netDialer := &net.Dialer{Timeout: dialTimeout}
	addr := hostPort(u)

	if u.Scheme == "https" {
		// ServerName is left empty on purpose: crypto/tls fills it from the
		// dial address, which is what the certificate has to match
		d := &tls.Dialer{NetDialer: netDialer, Config: tlsCfg}
		return d.DialContext(ctx, "tcp", addr)
	}
	return netDialer.DialContext(ctx, "tcp", addr)
}

func upgrade(conn net.Conn, u *url.URL, hello protocol.Hello) (net.Conn, error) {
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", protocol.UpgradeProto)
	hello.SetHeaders(req.Header)

	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return nil, err
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("upgrade rejected: %s", resp.Status)
	}

	// the tunnel is long lived: drop the handshake deadline
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	// the server starts pinging the moment it writes the 101, so its first
	// frames can land in the same segment and end up buffered here
	return protocol.WithBuffered(conn, br), nil
}

// handleStream dispatches one stream opened by the server. Each kind is a
// self-contained handler; adding a capability means adding a case here.
func handleStream(stream net.Conn) {
	defer stream.Close()

	br := bufio.NewReader(stream)
	kind, err := protocol.ReadStreamHeader(br)
	if err != nil {
		slog.Warn("stream: unreadable header", "err", err)
		return
	}

	switch kind {
	case protocol.StreamEcho:
		if _, err := io.Copy(stream, br); err != nil {
			slog.Warn("echo failed", "err", err)
		}
	case protocol.StreamPTY:
		handlePTY(stream, br)
	case protocol.StreamRPC:
		handleRPC(stream, br)
	case protocol.StreamForward:
		handleForward(stream, br)
	default:
		slog.Warn("stream: unsupported kind", "kind", kind)
	}
}

func hostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "https" {
		return net.JoinHostPort(u.Hostname(), "443")
	}
	return net.JoinHostPort(u.Hostname(), "80")
}
