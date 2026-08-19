//go:build windows

package main

import (
	"context"
	"errors"
	"os/exec"
)

// Windows needs ConPTY rather than a pty device, which is a separate piece of
// work. Reporting it as unavailable keeps a Windows agent fully functional for
// everything else instead of failing to build.
func startPTY() (ptyTerm, error) {
	return nil, errors.New("interactive terminal is not supported on windows yet")
}

// shellCommand builds a one-shot command for the exec operation.
func shellCommand(ctx context.Context, command, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd", "/c", command)
	cmd.Dir = dir
	return cmd
}
