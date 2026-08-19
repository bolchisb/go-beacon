package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/hashicorp/yamux"
)

var errAgentNotRunning = errors.New("agent is not running")

// sun_path is 104 bytes on darwin and the BSDs, 108 on linux. Exceeding it
// fails as "bind: invalid argument", which explains nothing to whoever has to
// debug it on a machine they cannot log into.
const maxSocketPath = 100

// statusPayload is the whole contract of the control socket.
type statusPayload struct {
	Version    string     `json:"version"`
	AgentID    string     `json:"agent_id"`
	Server     string     `json:"server"`
	ConfigPath string     `json:"config_path"`
	PID        int        `json:"pid"`
	StartedAt  time.Time  `json:"started_at"`
	Connected  bool       `json:"connected"`
	Connecting bool       `json:"connecting"`
	Since      time.Time  `json:"since"`
	RTTms      *float64   `json:"rtt_ms"`
	Streams    int        `json:"streams"`
	BytesIn    uint64     `json:"bytes_in"`
	BytesOut   uint64     `json:"bytes_out"`
	LastError  string     `json:"last_error,omitempty"`
	NextRetry  *time.Time `json:"next_retry,omitempty"`
}

// agentState is the live picture of the running agent, published on the
// control socket.
type agentState struct {
	mu        sync.Mutex
	base      statusPayload
	sess      *yamux.Session
	conn      *protocol.CountingConn
	rttNanos  atomic.Int64
	lastError string
	nextRetry *time.Time
}

func newAgentState(agentID, server, configPath string) *agentState {
	st := &agentState{base: statusPayload{
		Version:    version,
		AgentID:    agentID,
		Server:     server,
		ConfigPath: configPath,
		PID:        os.Getpid(),
		StartedAt:  time.Now(),
		// Since has to start somewhere: an agent that has never managed to
		// connect is offline as of now, not since the zero time, which renders
		// as a couple of centuries.
		Since: time.Now(),
	}}
	st.rttNanos.Store(-1)
	return st
}

func (s *agentState) connected(sess *yamux.Session, conn *protocol.CountingConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sess, s.conn = sess, conn
	s.base.Connected = true
	s.base.Connecting = false
	s.base.Since = time.Now()
	s.lastError = ""
	s.nextRetry = nil
	s.rttNanos.Store(-1)
}

func (s *agentState) disconnected(err error, nextRetry time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sess, s.conn = nil, nil
	if s.base.Connected {
		s.base.Since = time.Now()
	}
	s.base.Connected = false
	s.base.Connecting = false
	if err != nil {
		s.lastError = err.Error()
	}
	if nextRetry.IsZero() {
		s.nextRetry = nil
	} else {
		t := nextRetry
		s.nextRetry = &t
	}
	s.rttNanos.Store(-1)
}

// connecting marks an attempt as in flight, so status can say so instead of
// reporting a retry time that has already passed.
func (s *agentState) connecting() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.base.Connecting = true
	s.nextRetry = nil
}

func (s *agentState) snapshot() statusPayload {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := s.base
	out.LastError = s.lastError
	out.NextRetry = s.nextRetry
	if s.sess != nil {
		out.Streams = s.sess.NumStreams()
		out.BytesIn = s.conn.In()
		out.BytesOut = s.conn.Out()
		if ns := s.rttNanos.Load(); ns >= 0 {
			ms := float64(ns) / float64(time.Millisecond)
			out.RTTms = &ms
		}
	}
	return out
}

// serveControl exposes the state over a unix socket. The API is read only, so
// the socket is left world readable on purpose: a developer must be able to run
// `beacon status` without elevating, while the agent itself runs as a service.
func serveControl(ctx context.Context, st *agentState) (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(st.snapshot())
	})

	var lastErr error
	for _, path := range socketPaths() {
		if len(path) >= maxSocketPath {
			lastErr = fmt.Errorf("socket path is %d characters, the limit is %d: %s",
				len(path), maxSocketPath, path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			lastErr = err
			continue
		}
		// a socket left behind by a crash would block the listen
		os.Remove(path)

		l, err := net.Listen("unix", path)
		if err != nil {
			lastErr = err
			continue
		}
		if err := os.Chmod(path, 0o666); err != nil {
			slog.Debug("control socket chmod failed", "path", path, "err", err)
		}

		srv := &http.Server{Handler: mux}
		go func() {
			<-ctx.Done()
			srv.Close()
			os.Remove(path)
		}()
		go srv.Serve(l)
		return path, nil
	}
	return "", lastErr
}

// fetchStatus asks the running agent for its state, trying each socket the
// agent might have been able to create.
func fetchStatus() (statusPayload, string, error) {
	for _, path := range socketPaths() {
		if len(path) >= maxSocketPath {
			continue
		}
		client := &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", path)
				},
			},
		}
		resp, err := client.Get("http://beacon/status")
		if err != nil {
			continue
		}
		var out statusPayload
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			continue
		}
		return out, path, nil
	}
	return statusPayload{}, "", errAgentNotRunning
}
