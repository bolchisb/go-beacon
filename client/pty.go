package main

import (
	"bufio"
	"io"
	"log/slog"
	"net"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/bolchisb/go-beacon/internal/supervise"
)

// ptyTerm is one interactive terminal. The two implementations differ only in
// how the operating system hands out a pseudo-terminal.
type ptyTerm interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

// handlePTY runs one terminal for the lifetime of a relay stream. Terminal
// output goes back raw; the relay's side is framed so a window resize can
// travel in band with the keystrokes.
func handlePTY(stream net.Conn, br *bufio.Reader) {
	term, err := startPTY()
	if err != nil {
		slog.Warn("pty: cannot start", "err", err)
		// the browser has a terminal open and deserves to be told why it is empty
		io.WriteString(stream, "beacon: "+err.Error()+"\r\n")
		return
	}
	defer term.Close()

	slog.Info("pty: session opened")
	defer slog.Info("pty: session closed")

	// Closing the stream when the shell exits is what makes the browser tab
	// notice; without it the socket would sit open around a dead process.
	supervise.Go("pty-pump", func() {
		io.Copy(stream, term)
		stream.Close()
	})

	for {
		typ, payload, err := protocol.ReadPTYFrame(br)
		if err != nil {
			return
		}
		switch typ {
		case protocol.PTYData:
			if _, err := term.Write(payload); err != nil {
				return
			}
		case protocol.PTYResize:
			cols, rows, err := protocol.PTYSize(payload)
			if err != nil {
				slog.Warn("pty: bad resize frame", "err", err)
				continue
			}
			if err := term.Resize(cols, rows); err != nil {
				slog.Warn("pty: resize failed", "err", err)
			}
		default:
			slog.Warn("pty: unknown frame type", "type", typ)
		}
	}
}
