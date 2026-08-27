package main

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// handleClipboardImage puts an image on a target machine's clipboard.
//
// It exists for one gesture: pasting a screenshot into the browser terminal.
// The terminal itself cannot carry it -- a terminal carries keystrokes, and a
// paste of an image has none -- so the image goes around the terminal, through
// this endpoint, and arrives on the clipboard of the machine the terminal is
// attached to. Whatever is running in that shell then finds it where it
// expects: in the clipboard of the machine it is running on.
//
// The body is the raw image, not JSON. A screenshot is already bytes, and
// base64 in the request would cost a third of the size on a path where size is
// the only real constraint.
func (s *Server) handleClipboardImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// One byte over the limit is read on purpose: it is the difference between
	// "this is too big" and silently pasting a truncated image.
	raw, err := io.ReadAll(io.LimitReader(r.Body, protocol.MaxClipboardImageBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the image"})
		return
	}
	if len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no image in the request"})
		return
	}
	if len(raw) > protocol.MaxClipboardImageBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
			"error": "that image is over the 8 MiB limit"})
		return
	}

	resp, err := s.agentRPC(id, protocol.RPCRequest{
		Op:      protocol.OpClipboardWriteImage,
		Content: base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if resp.Error != "" {
		// The agent's own words. A machine with no graphical session says so,
		// and that is a different problem from the tunnel being down.
		writeJSON(w, http.StatusConflict, map[string]string{"error": resp.Error})
		return
	}

	slog.Info("clipboard image", "agent", id, "bytes", resp.Written)
	s.events.Publish(Event{Type: "clipboard", AgentID: id,
		Message: "an image was placed on the clipboard"})
	writeJSON(w, http.StatusOK, map[string]any{"written": resp.Written})
}
