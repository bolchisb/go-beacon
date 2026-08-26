// Package supervise keeps one failing unit of work from taking the process
// with it.
//
// Go has no supervision. An unrecovered panic in any goroutine terminates the
// whole program, so a nil pointer while handling one agent's log would
// disconnect every other agent on the relay, and a bad frame from one terminal
// would take down an agent on a machine nobody can reach by other means.
//
// The unit of isolation here is the goroutine: it dies, it says why, and
// whatever supervises it decides what happens next. Sessions are already
// supervised by a reconnect loop; streams are cheap and simply end.
package supervise

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// Go runs fn in a goroutine that cannot bring the process down. The name says
// which unit died, because a stack trace alone rarely does.
func Go(name string, fn func()) {
	go func() {
		defer Recover(name)
		fn()
	}()
}

// Recover is the deferred half of Go, exported for goroutines that need to do
// their own cleanup as well.
func Recover(name string) {
	if r := recover(); r != nil {
		slog.Error("recovered from a panic",
			"unit", name,
			"panic", fmt.Sprint(r),
			"stack", string(debug.Stack()))
	}
}

// Do runs fn on the calling goroutine and turns a panic into an error, so a
// caller that already knows how to retry can treat a crash like any other
// failure.
func Do(name string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("recovered from a panic",
				"unit", name,
				"panic", fmt.Sprint(r),
				"stack", string(debug.Stack()))
			err = fmt.Errorf("%s panicked: %v", name, r)
		}
	}()
	return fn()
}
