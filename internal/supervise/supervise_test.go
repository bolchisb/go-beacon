package supervise

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The whole point: a panicking unit must not end the process, and the ones
// beside it must keep running.
func TestGoIsolatesAPanic(t *testing.T) {
	var wg sync.WaitGroup
	survived := make(chan string, 2)

	wg.Add(3)
	Go("panicking", func() { defer wg.Done(); panic("boom") })
	Go("neighbour-a", func() { defer wg.Done(); survived <- "a" })
	Go("neighbour-b", func() { defer wg.Done(); survived <- "b" })

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("units did not finish")
	}
	if len(survived) != 2 {
		t.Fatalf("%d neighbours survived, want 2", len(survived))
	}
}

func TestDoTurnsAPanicIntoAnError(t *testing.T) {
	err := Do("unit", func() error { panic("boom") })
	if err == nil {
		t.Fatal("a panic should surface as an error")
	}
	if !strings.Contains(err.Error(), "unit") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("the error should name the unit and the cause, got %q", err)
	}
}

func TestDoPassesOrdinaryResultsThrough(t *testing.T) {
	if err := Do("unit", func() error { return nil }); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	sentinel := errors.New("ordinary failure")
	if err := Do("unit", func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("want the original error, got %v", err)
	}
}
