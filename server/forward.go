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

// handleForward joins an operator to one forwardable service on one agent,
// such as remote desktop.
//
// The operator's side arrives as a WebSocket rather than as a listening port on
// the relay. The relay is reachable on 443 over TLS and nothing else: a second
// listening port would be published nowhere and blocked everywhere. The port a
// desktop client needs is opened next to that client instead, by `beacon
// forward`, and its bytes ride this connection.
//
// The relay interprets nothing in between.
func (s *Server) handleForward(w http.ResponseWriter, r *http.Request) {
	id, service := r.PathValue("id"), r.PathValue("service")

	// Open the tunnel stream before the upgrade. Once this becomes a WebSocket
	// there is no way left to report an HTTP status, and "that agent is
	// offline" is exactly what the operator needs to hear.
	stream, err := s.openAgentStream(id, protocol.StreamForward)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errAgentOffline) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	defer stream.Close()

	if err := protocol.WriteForwardTarget(stream, service); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.Warn("forward: websocket accept failed", "agent", id, "service", service, "err", err)
		return
	}
	defer c.CloseNow()

	slog.Info("forward opened", "agent", id, "service", service, "remote", r.RemoteAddr)
	s.events.Publish(Event{Type: "forward", AgentID: id,
		Message: service + " session opened from " + r.RemoteAddr})
	defer s.events.Publish(Event{Type: "forward", AgentID: id, Message: service + " session closed"})

	ws := websocket.NetConn(r.Context(), c, websocket.MessageBinary)

	supervise.Go("forward-pump:"+id, func() {
		io.Copy(stream, ws)
		stream.Close()
	})
	io.Copy(ws, stream)
}
