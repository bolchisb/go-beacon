package main

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/bolchisb/go-beacon/internal/supervise"
	"github.com/hashicorp/yamux"
)

// Transport is what the relay can honestly say about how a connection reached
// it, and the difference between the first two answers is the whole reason it
// is worth showing at all.
type Transport string

const (
	// TransportTLS: this relay terminated TLS itself, so the handshake is
	// something it observed rather than something it was told.
	TransportTLS Transport = "tls"

	// TransportProxyTLS: the connection arrived in plain text carrying
	// X-Forwarded-Proto: https. That is a claim in a header, and a header is
	// written by whoever sent the request -- which on this path is the agent.
	// A relay behind a proxy that overwrites the header is genuinely encrypted;
	// one reachable directly is being told a story. The relay cannot tell those
	// apart, so it reports the claim and does not dress it up as a fact.
	TransportProxyTLS Transport = "proxy-tls"

	// TransportPlain: no TLS, and nothing claiming any.
	TransportPlain Transport = "plain"
)

// agentRec is the server-side record of one agent. Records survive
// disconnection so the UI can show who dropped and how often they flap.
type agentRec struct {
	hello      protocol.Hello
	remoteAddr string
	session    *yamux.Session
	conn       *protocol.CountingConn

	transport Transport
	// transportDetail is what to tell an operator hovering the padlock: the
	// negotiated version when this relay saw the handshake, or which proxy
	// vouched for it when it did not.
	transportDetail string

	online     bool
	since      time.Time // when the current online/offline state began
	reconnects int
	rttNanos   atomic.Int64 // -1 when not measured yet
}

// AgentView is the JSON shape served to the dashboard.
type AgentView struct {
	ID           string    `json:"id"`
	Hostname     string    `json:"hostname"`
	OS           string    `json:"os"`
	Arch         string    `json:"arch"`
	Version      string    `json:"version"`
	RemoteAddr   string    `json:"remote_addr"`
	Online       bool      `json:"online"`
	Since        time.Time `json:"since"`
	SinceSeconds float64   `json:"since_seconds"`
	Reconnects   int       `json:"reconnects"`
	Streams      int       `json:"streams"`
	// Transport is "tls", "proxy-tls" or "plain"; TransportDetail explains it
	// in words, for the tooltip on the padlock.
	Transport       string   `json:"transport"`
	TransportDetail string   `json:"transport_detail,omitempty"`
	BytesIn         uint64   `json:"bytes_in"`
	BytesOut        uint64   `json:"bytes_out"`
	RTTms           *float64 `json:"rtt_ms"`
}

type Registry struct {
	mu     sync.Mutex
	agents map[string]*agentRec
}

func newRegistry() *Registry {
	return &Registry{agents: make(map[string]*agentRec)}
}

// Connect registers a freshly established session. If the agent id is already
// online the previous session is closed: last connection wins, which is what
// you want when a client reconnects before the old socket has timed out.
func (r *Registry) Connect(h protocol.Hello, remoteAddr string, sess *yamux.Session, conn *protocol.CountingConn, transport Transport, transportDetail string) (rec *agentRec, reconnect bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var stale *yamux.Session
	rec, existed := r.agents[h.AgentID]
	if existed {
		stale = rec.session
		rec.reconnects++
		reconnect = true
	} else {
		rec = &agentRec{}
		r.agents[h.AgentID] = rec
	}

	rec.hello = h
	rec.remoteAddr = remoteAddr
	rec.transport = transport
	rec.transportDetail = transportDetail
	rec.session = sess
	rec.conn = conn
	rec.online = true
	rec.since = time.Now()
	rec.rttNanos.Store(-1)

	if stale != nil {
		supervise.Go("close-stale:"+h.AgentID, func() { stale.Close() })
	}
	return rec, reconnect
}

// Disconnect marks an agent offline, but only if the session that died is
// still the current one — a late close from a superseded session must not
// knock the fresh connection offline.
func (r *Registry) Disconnect(id string, sess *yamux.Session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.agents[id]
	if !ok || rec.session != sess || !rec.online {
		return false
	}
	rec.online = false
	rec.since = time.Now()
	rec.rttNanos.Store(-1)
	return true
}

// Session returns the live session for an agent, if it is online.
func (r *Registry) Session(id string) (*yamux.Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.agents[id]
	if !ok || !rec.online || rec.session == nil {
		return nil, false
	}
	return rec.session, true
}

func (r *Registry) Snapshot() []AgentView {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	out := make([]AgentView, 0, len(r.agents))
	for id, rec := range r.agents {
		v := AgentView{
			ID:           id,
			Hostname:     rec.hello.Hostname,
			OS:           rec.hello.OS,
			Arch:         rec.hello.Arch,
			Version:      rec.hello.Version,
			RemoteAddr:   rec.remoteAddr,
			Online:       rec.online,
			Since:        rec.since,
			SinceSeconds: now.Sub(rec.since).Seconds(),
			Reconnects:   rec.reconnects,
		}
		if rec.online {
			// Only for a live session. On a record that has dropped, the
			// transport describes a connection that no longer exists, and a
			// padlock next to "disconnected" would be claiming something about
			// nothing.
			v.Transport = string(rec.transport)
			v.TransportDetail = rec.transportDetail
			v.Streams = rec.session.NumStreams()
			v.BytesIn = rec.conn.In()
			v.BytesOut = rec.conn.Out()
			if ns := rec.rttNanos.Load(); ns >= 0 {
				ms := float64(ns) / float64(time.Millisecond)
				v.RTTms = &ms
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) Counts() (online, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, rec := range r.agents {
		total++
		if rec.online {
			online++
		}
	}
	return online, total
}
