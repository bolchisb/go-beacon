package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/hashicorp/yamux"
)

const (
	// how often the server measures tunnel round-trip time
	pingInterval = 5 * time.Second
	// the upgrade handshake must complete quickly; the tunnel itself has no deadline
	handshakeTimeout = 10 * time.Second
	echoTimeout      = 5 * time.Second
	echoPayloadSize  = 32
)

// handleAgentConnect turns an HTTP request into a raw tunnel: the agent asks
// for an upgrade, the server hijacks the connection and runs yamux over it.
// Going through HTTP rather than a bare TCP port is what lets agents behind a
// corporate proxy reach the relay at all.
func (s *Server) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	hello, err := s.identify(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), protocol.UpgradeProto) {
		http.Error(w, "expected Upgrade: "+protocol.UpgradeProto, http.StatusUpgradeRequired)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}

	tunnel, err := s.completeUpgrade(conn, brw.Reader)
	if err != nil {
		slog.Warn("upgrade failed", "agent", hello.AgentID, "remote", r.RemoteAddr, "err", err)
		conn.Close()
		return
	}

	counted := &countingConn{Conn: tunnel}
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	sess, err := yamux.Server(counted, cfg)
	if err != nil {
		slog.Warn("yamux setup failed", "agent", hello.AgentID, "err", err)
		conn.Close()
		return
	}

	go s.serveSession(hello, r.RemoteAddr, sess, counted)
}

// identify is the single place agent identity is established. Today it comes
// from the hello headers; under mTLS it will come from
// r.TLS.PeerCertificates and nothing else in the server has to change.
func (s *Server) identify(r *http.Request) (protocol.Hello, error) {
	return protocol.HelloFromHeaders(r.Header)
}

func (s *Server) completeUpgrade(conn net.Conn, br *bufio.Reader) (net.Conn, error) {
	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: " + protocol.UpgradeProto + "\r\n" +
		"Connection: Upgrade\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		return nil, err
	}
	// the tunnel is long lived: drop the handshake deadline
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	// an agent may pipeline its first frames behind the request
	return protocol.WithBuffered(conn, br), nil
}

// serveSession owns an agent session for its whole life and is what notices
// when it dies.
func (s *Server) serveSession(h protocol.Hello, remote string, sess *yamux.Session, conn *countingConn) {
	rec, reconnect := s.registry.Connect(h, remote, sess, conn)

	msg := "connected from " + remote
	if reconnect {
		msg = "reconnected from " + remote
	}
	slog.Info("agent online", "agent", h.AgentID, "host", h.Hostname, "remote", remote, "reconnect", reconnect)
	s.events.Publish(Event{Type: "connected", AgentID: h.AgentID, Message: msg})

	go s.pingLoop(rec, sess)

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			break
		}
		// Phase 1 has no agent-initiated services yet; log it and hang up so a
		// buggy client cannot leak streams.
		s.events.Publish(Event{
			Type:    "stream",
			AgentID: h.AgentID,
			Message: fmt.Sprintf("agent opened stream %d (no handler yet)", stream.StreamID()),
		})
		stream.Close()
	}

	sess.Close()
	if s.registry.Disconnect(h.AgentID, sess) {
		slog.Info("agent offline", "agent", h.AgentID)
		s.events.Publish(Event{Type: "disconnected", AgentID: h.AgentID, Message: "session closed"})
	}
}

// pingLoop keeps a live RTT figure per agent. It also proves the tunnel is
// carrying traffic, not merely that a socket is still open.
func (s *Server) pingLoop(rec *agentRec, sess *yamux.Session) {
	measure := func() bool {
		rtt, err := sess.Ping()
		if err != nil {
			return false
		}
		rec.rttNanos.Store(int64(rtt))
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

// echoTest opens a real stream and bounces a random payload off the agent.
// Unlike a yamux ping this exercises multiplexing end to end.
func echoTest(sess *yamux.Session) (time.Duration, error) {
	stream, err := sess.OpenStream()
	if err != nil {
		return 0, err
	}
	defer stream.Close()

	if err := stream.SetDeadline(time.Now().Add(echoTimeout)); err != nil {
		return 0, err
	}

	payload := make([]byte, echoPayloadSize)
	if _, err := rand.Read(payload); err != nil {
		return 0, err
	}

	start := time.Now()
	if err := protocol.WriteStreamHeader(stream, protocol.StreamEcho); err != nil {
		return 0, err
	}
	if _, err := stream.Write(payload); err != nil {
		return 0, err
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, got); err != nil {
		return 0, err
	}
	rtt := time.Since(start)

	if !bytes.Equal(got, payload) {
		return 0, errors.New("echo payload mismatch")
	}
	return rtt, nil
}
