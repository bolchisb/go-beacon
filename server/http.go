package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed ui
var uiFS embed.FS

// Server wires the two audiences together on one port: the dashboard under
// /ui and /api, the agents under /agent/connect.
type Server struct {
	cfg        Config
	registry   *Registry
	events     *EventBus
	mcp        *mcp.Server
	auth       *auth
	vault      *vault
	ops        *operatorStore
	agentKeys  *agentKeys
	proxies    *proxySet
	challenges *challenges
	startedAt  time.Time
}

func newServer(cfg Config) *Server {
	v := newVault(cfg)
	ops := newOperatorStore(v, cfg.StateDir)
	proxies := parseProxies(cfg.TrustedProxies)
	s := &Server{
		cfg:        cfg,
		registry:   newRegistry(),
		events:     newEventBus(),
		auth:       newAuth(cfg.AdminToken, ops, proxies),
		vault:      v,
		ops:        ops,
		agentKeys:  newAgentKeys(v),
		proxies:    proxies,
		challenges: newChallenges(),
		startedAt:  time.Now(),
	}
	s.mcp = newMCPServer(s)
	return s
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// agent tunnel
	mux.HandleFunc(protocol.ConnectPath, s.handleAgentConnect)
	mux.HandleFunc("GET "+protocol.ChallengePath, s.handleAgentChallenge)

	// Enrolment. Open to reach, but it proves nothing on its own: it checks
	// operator credentials in the body before it will sign anything.
	mux.HandleFunc("POST "+protocol.EnrollPath, s.handleEnroll)

	// dashboard API
	mux.HandleFunc("GET /api/server", s.handleServerInfo)
	mux.HandleFunc("GET /api/agents", s.handleAgents)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/agents/{id}/ping", s.handleAgentPing)
	mux.HandleFunc("POST /api/agents/{id}/kick", s.handleAgentKick)
	mux.HandleFunc("GET /api/agents/{id}/shell", s.handleShell)
	mux.HandleFunc("GET /api/agents/{id}/forward/{service}", s.handleForward)
	mux.HandleFunc("POST /api/agents/{id}/clipboard/image", s.handleClipboardImage)

	// MCP, for an assistant running on a developer's laptop. No method is given
	// because the transport uses POST for calls and GET for the event stream,
	// and both belong to the same endpoint.
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.mcp }, nil))

	// dashboard
	ui, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err) // embedded at build time; cannot fail at runtime
	}
	mux.Handle("GET /ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(ui))))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})

	mux.HandleFunc("GET /healthz", s.handleHealth)

	// Operator sign-in. Registered on the mux like everything else, and exempt
	// from the gate by name in auth.open rather than by living outside it.
	mux.HandleFunc("POST /api/login", s.auth.handleLogin)
	mux.HandleFunc("POST /api/logout", s.auth.handleLogout)
	mux.HandleFunc("POST /api/bootstrap", s.auth.handleBootstrap)

	// Behind the gate, unlike the three above.
	mux.HandleFunc("POST /api/operator/password", s.auth.handleChangePassword)

	return s.auth.protect(mux)
}

type serverInfo struct {
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	AgentsOnline  int     `json:"agents_online"`
	AgentsKnown   int     `json:"agents_known"`
	AuthEnabled   bool    `json:"auth_enabled"`
	Operator      string  `json:"operator,omitempty"`
	Vault         string  `json:"vault"`
}

func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	online, total := s.registry.Counts()
	writeJSON(w, http.StatusOK, serverInfo{
		Version:       version,
		UptimeSeconds: time.Since(s.startedAt).Seconds(),
		AgentsOnline:  online,
		AgentsKnown:   total,
		AuthEnabled:   s.auth.enabled(),
		Operator:      s.operatorName(),
		Vault:         string(s.vault.SealStatus()),
	})
}

func (s *Server) operatorName() string {
	if rec := s.ops.current(); rec != nil {
		return rec.Username
	}
	return ""
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.registry.Snapshot())
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	online, total := s.registry.Counts()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"agents_online": online,
		"agents_known":  total,
	})
}

func (s *Server) handleAgentPing(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.registry.Session(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not connected"})
		return
	}
	rtt, err := echoTest(sess)
	if err != nil {
		s.events.Publish(Event{Type: "ping", AgentID: id, Message: "echo test failed: " + err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	ms := float64(rtt) / float64(time.Millisecond)
	s.events.Publish(Event{Type: "ping", AgentID: id, Message: fmt.Sprintf("echo round-trip %.2f ms", ms)})
	writeJSON(w, http.StatusOK, map[string]float64{"rtt_ms": ms})
}

func (s *Server) handleAgentKick(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.registry.Session(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not connected"})
		return
	}
	s.events.Publish(Event{Type: "kick", AgentID: id, Message: "session closed from dashboard"})
	sess.Close()
	writeJSON(w, http.StatusOK, map[string]string{"status": "closed"})
}

// handleEvents streams the activity feed to the browser. SSE rather than
// WebSocket: the flow is one way, so framing would buy nothing.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		slog.Debug("sse: cannot clear write deadline", "err", err)
	}

	history, ch, unsubscribe := s.events.Subscribe()
	defer unsubscribe()

	for _, e := range history {
		if err := writeSSE(w, e); err != nil {
			return
		}
	}
	if err := rc.Flush(); err != nil {
		return
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if err := writeSSE(w, e); err != nil {
				return
			}
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

func writeSSE(w io.Writer, e Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}

// decodeJSON reads a request body with a bound on its size, so a malformed or
// hostile caller cannot make the relay allocate without limit.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("could not read the request: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("json encode failed", "err", err)
	}
}
