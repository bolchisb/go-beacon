//go:build windows

package main

import (
	"os"
	"time"

	"github.com/charmbracelet/x/term"
)

// Windows has no SIGWINCH, so the size is polled instead. Half a second is
// slow enough to cost nothing and fast enough that a resize feels immediate.
const resizePollInterval = 500 * time.Millisecond

func watchResize(onChange func()) func() {
	done := make(chan struct{})

	go func() {
		lastCols, lastRows, _ := term.GetSize(os.Stdout.Fd())
		t := time.NewTicker(resizePollInterval)
		defer t.Stop()

		for {
			select {
			case <-t.C:
				cols, rows, err := term.GetSize(os.Stdout.Fd())
				if err == nil && (cols != lastCols || rows != lastRows) {
					lastCols, lastRows = cols, rows
					onChange()
				}
			case <-done:
				return
			}
		}
	}()

	return func() { close(done) }
}
