//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	xpty "github.com/aymanbagabas/go-pty"
)

type windowsPTY struct {
	pty xpty.Pty
	cmd *xpty.Cmd
}

// startPTY opens a ConPTY and runs a shell attached to it. Windows has no pty
// device; ConPTY is the pseudoconsole API that replaced screen scraping, and it
// is what makes colours, cursor movement and window size behave the same way
// they do on unix.
func startPTY() (ptyTerm, error) {
	p, err := xpty.New()
	if err != nil {
		return nil, fmt.Errorf("cannot open a console (needs windows 10 1809 or newer): %w", err)
	}

	shell := windowsShell()
	cmd := p.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if err := cmd.Start(); err != nil {
		p.Close()
		return nil, fmt.Errorf("cannot start %s: %w", shell, err)
	}
	return &windowsPTY{pty: p, cmd: cmd}, nil
}

// windowsShell prefers PowerShell, which is what an operator on a Windows host
// expects to land in, and falls back to the command processor.
func windowsShell() string {
	for _, candidate := range []string{"pwsh.exe", "powershell.exe"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	if comspec := os.Getenv("COMSPEC"); comspec != "" {
		return comspec
	}
	return "cmd.exe"
}

func (p *windowsPTY) Read(b []byte) (int, error)  { return p.pty.Read(b) }
func (p *windowsPTY) Write(b []byte) (int, error) { return p.pty.Write(b) }

func (p *windowsPTY) Resize(cols, rows uint16) error {
	return p.pty.Resize(int(cols), int(rows))
}

func (p *windowsPTY) Close() error {
	err := p.pty.Close()
	if p.cmd.Process != nil {
		// closing the console does not always take the shell with it
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	return err
}

// shellCommand builds a one-shot command for the exec operation.
func shellCommand(ctx context.Context, command, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd", "/c", command)
	cmd.Dir = dir
	return cmd
}
