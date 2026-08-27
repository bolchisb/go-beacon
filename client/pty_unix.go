//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

type unixPTY struct {
	f   *os.File
	cmd *exec.Cmd

	// A process can only be waited for once, and both Close and the session
	// that owns the terminal need the answer.
	waitOnce sync.Once
	code     int
}

func startPTY() (ptyTerm, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return startPTYWith(shell, []string{"-l"}, nil)
}

// startPTYWith runs a specific program on a pty. The ssh server needs this to
// honour a client's TERM and to run a requested command, rather than always
// dropping into a login shell.
func startPTYWith(name string, args []string, env []string) (ptyTerm, error) {
	cmd := exec.Command(name, args...)
	// Without TERM the shell assumes a dumb terminal and every full-screen
	// program the developer actually needs refuses to draw.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.Env = append(cmd.Env, env...)

	f, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	return &unixPTY{f: f, cmd: cmd}, nil
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *unixPTY) Resize(cols, rows uint16) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: cols, Rows: rows})
}

func (p *unixPTY) Wait() int {
	p.waitOnce.Do(func() { p.code = exitCodeOf(p.cmd.Wait()) })
	return p.code
}

func (p *unixPTY) Close() error {
	err := p.f.Close()
	if p.cmd.Process != nil {
		// the shell may be sitting in a child that ignores EOF on the pty
		_ = p.cmd.Process.Kill()
		p.Wait()
	}
	return err
}

// shellCommand builds a one-shot command for the exec operation.
func shellCommand(ctx context.Context, command, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = dir
	return cmd
}
