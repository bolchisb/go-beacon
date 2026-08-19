package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// shipQueue is small on purpose. If the relay stops draining, dropping old
// warnings is better than letting the logger block whatever was reporting one.
const shipQueue = 128

// logSink holds the stream warnings are currently written to. It is shared by
// every handler derived from the root one.
type logSink struct {
	lines chan protocol.LogLine

	mu sync.Mutex
	w  io.Writer
}

// logHandler forwards warnings and errors to the relay in addition to writing
// them locally. Anything below warning stays local: the relay already records
// sessions opening and closing, and echoing that back would bury the one line
// someone is looking for.
type logHandler struct {
	base slog.Handler
	sink *logSink
}

func newLogHandler(base slog.Handler) *logHandler {
	sink := &logSink{lines: make(chan protocol.LogLine, shipQueue)}
	go sink.run()
	return &logHandler{base: base, sink: sink}
}

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *logHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		var parts []string
		r.Attrs(func(a slog.Attr) bool {
			parts = append(parts, protocol.FormatAttr(a.Key, a.Value.Any()))
			return true
		})
		line := protocol.LogLine{
			Time:  r.Time,
			Level: r.Level.String(),
			Msg:   r.Message,
			Attrs: protocol.JoinAttrs(parts),
		}
		select {
		case h.sink.lines <- line:
		default: // the relay is not keeping up; local logging still happened
		}
	}
	return h.base.Handle(ctx, r)
}

// WithAttrs and WithGroup keep the same sink so a derived logger still ships.
func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logHandler{base: h.base.WithAttrs(attrs), sink: h.sink}
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	return &logHandler{base: h.base.WithGroup(name), sink: h.sink}
}

// attach starts sending to a stream; detach stops, but only if that same
// stream is still the current one, so a late close cannot silence a newer
// session.
func (h *logHandler) attach(w io.Writer) {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	h.sink.w = w
}

func (h *logHandler) detach(w io.Writer) {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	if h.sink.w == w {
		h.sink.w = nil
	}
}

// run owns the writing, so a stalled relay cannot block a caller that is only
// trying to log.
func (s *logSink) run() {
	for line := range s.lines {
		s.mu.Lock()
		w := s.w
		s.mu.Unlock()
		if w == nil {
			continue
		}
		if err := json.NewEncoder(w).Encode(line); err != nil {
			s.mu.Lock()
			if s.w == w {
				s.w = nil
			}
			s.mu.Unlock()
		}
	}
}
