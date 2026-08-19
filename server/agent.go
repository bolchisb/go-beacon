package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
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

	counted := protocol.NewCountingConn(tunnel)
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
func (s *Server) serveSession(h protocol.Hello, remote string, sess *yamux.Session, conn *protocol.CountingConn) {
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
		go s.handleAgentStream(h.AgentID, stream)
	}

	sess.Close()
	if s.registry.Disconnect(h.AgentID, sess) {
		slog.Info("agent offline", "agent", h.AgentID)
		s.events.Publish(Event{Type: "disconnected", AgentID: h.AgentID, Message: "session closed"})
	}
}

// handleAgentStream serves a stream the agent opened. The relay is the only
// place an operator can see an agent's own warnings, so it reads them here
// rather than hanging up.
func (s *Server) handleAgentStream(agentID string, stream net.Conn) {
	defer stream.Close()

	br := bufio.NewReader(stream)
	kind, err := protocol.ReadStreamHeader(br)
	if err != nil {
		return
	}

	switch kind {
	case protocol.StreamLog:
		s.consumeAgentLog(agentID, br)
	default:
		s.events.Publish(Event{
			Type:    "stream",
			AgentID: agentID,
			Message: fmt.Sprintf("agent opened an unknown stream %q", kind),
		})
	}
}

// consumeAgentLog turns the agent's warnings into dashboard events. Only
// warnings and errors are sent, so this cannot drown the feed.
func (s *Server) consumeAgentLog(agentID string, br *bufio.Reader) {
	dec := json.NewDecoder(br)
	for {
		var line protocol.LogLine
		if err := dec.Decode(&line); err != nil {
			return
		}
		s.events.Publish(Event{
			Type:    eventTypeForLevel(line.Level),
			AgentID: agentID,
			Message: line.Text(),
			At:      line.Time,
		})
	}
}

func eventTypeForLevel(level string) string {
	if strings.HasPrefix(level, "ERROR") {
		return "error"
	}
	return "warn"
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
