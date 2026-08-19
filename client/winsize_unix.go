//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize calls onChange whenever the terminal is resized, and returns a
// function that stops watching.
func watchResize(onChange func()) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				onChange()
			case <-done:
				return
			}
		}
	}()

	return func() {
		signal.Stop(ch)
		close(done)
	}
}
