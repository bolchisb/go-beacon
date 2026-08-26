package main

import (
	"bytes"
	"sync"
	"testing"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

func TestShellURLDerivesTheWebSocketEndpoint(t *testing.T) {
	cases := []struct{ server, want string }{
		{"https://relay.example.com", "wss://relay.example.com/api/agents/target-01/shell"},
		{"http://127.0.0.1:8080", "ws://127.0.0.1:8080/api/agents/target-01/shell"},
		{"https://relay.example.com/?x=1", "wss://relay.example.com/api/agents/target-01/shell"},
	}
	for _, tc := range cases {
		got, err := shellURL(tc.server, "target-01")
		if err != nil {
			t.Fatalf("%s: %v", tc.server, err)
		}
		if got != tc.want {
			t.Fatalf("%s -> %s, want %s", tc.server, got, tc.want)
		}
	}

	for _, bad := range []string{"ftp://relay", "relay.example.com", ""} {
		if _, err := shellURL(bad, "target-01"); err == nil {
			t.Fatalf("%q should have been rejected", bad)
		}
	}
}

// Keystrokes and resizes share one connection, and a frame is a header
// followed by a payload. Two writers interleaving would corrupt both, so the
// writer has to serialise them.
func TestFrameWriterKeepsFramesIntactUnderConcurrency(t *testing.T) {
	var buf bytes.Buffer
	w := &frameWriter{w: &buf}

	const rounds = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			w.data([]byte("keystroke"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			w.mu.Lock()
			protocol.WritePTYFrame(w.w, protocol.PTYResize, protocol.PTYResizePayload(80, 24))
			w.mu.Unlock()
		}
	}()
	wg.Wait()

	// every frame must read back cleanly, in order, with nothing torn
	r := bytes.NewReader(buf.Bytes())
	data, resize := 0, 0
	for {
		typ, payload, err := protocol.ReadPTYFrame(r)
		if err != nil {
			break
		}
		switch typ {
		case protocol.PTYData:
			if string(payload) != "keystroke" {
				t.Fatalf("torn data frame: %q", payload)
			}
			data++
		case protocol.PTYResize:
			cols, rows, err := protocol.PTYSize(payload)
			if err != nil || cols != 80 || rows != 24 {
				t.Fatalf("torn resize frame: %v %d %d", err, cols, rows)
			}
			resize++
		default:
			t.Fatalf("unknown frame type %d", typ)
		}
	}
	if data != rounds || resize != rounds {
		t.Fatalf("got %d data and %d resize frames, want %d of each", data, resize, rounds)
	}
}
