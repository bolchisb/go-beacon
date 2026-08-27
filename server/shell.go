package main

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/bolchisb/go-beacon/internal/supervise"
	"github.com/coder/websocket"
)

// handleShell bridges a browser terminal to an agent's pty stream. The relay
// interprets nothing: the browser frames what it sends, the agent answers raw
// terminal output, and this is a pipe in both directions.
func (s *Server) handleShell(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Open the tunnel stream before the upgrade. Once the connection becomes a
	// WebSocket there is no way left to report an HTTP status, and "that agent
	// is offline" is exactly what the browser needs to hear.
	stream, err := s.openAgentStream(id, protocol.StreamPTY)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errAgentOffline) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	defer stream.Close()

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.Warn("shell: websocket accept failed", "agent", id, "err", err)
		return
	}
	defer c.CloseNow()

	slog.Info("shell opened", "agent", id, "remote", r.RemoteAddr)
	s.events.Publish(Event{Type: "shell", AgentID: id, Message: "terminal opened from " + r.RemoteAddr})
	defer s.events.Publish(Event{Type: "shell", AgentID: id, Message: "terminal closed"})

	ws := websocket.NetConn(r.Context(), c, websocket.MessageBinary)

	// Closing the stream when the browser goes away is what stops the shell on
	// the far side; the reverse copy then returns and the handler unwinds.
	supervise.Go("shell-pump:"+id, func() {
		io.Copy(stream, ws)
		stream.Close()
	})
	io.Copy(ws, stream)
}
