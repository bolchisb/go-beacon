//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Per-user install: no elevation anywhere.
//
// A Windows service is machine-wide and privileged to register, which is the
// right shape for a jump box and the wrong one for a developer's own machine.
// This mode registers a scheduled task under the current user instead, with the
// binary and config in that user's profile. Nothing here needs an administrator.
//
// What it buys, beyond not needing admin: the agent runs inside the user's own
// session, which is the only place WSL distributions exist. A service under
// LocalSystem cannot see them at all.
//
// What it costs, and it is not small: the agent runs only while that user is
// logged on. After a reboot the machine is unreachable until somebody signs in.
// For a machine nobody sits at, use the service.

const userTaskName = "beacon-agent"

// userInstall copies the binary into the user's profile and registers a task
// that starts it at logon.
func userInstall(target string) error {
	if target == "" {
		return fmt.Errorf("no per-user install path: LOCALAPPDATA is not set")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := copyExecutable(target); err != nil {
		return err
	}

	// /RL LIMITED, explicitly: this must not quietly ask for elevation, and a
	// task that runs elevated would land back in a context WSL cannot use.
	out, err := exec.Command("schtasks.exe", "/Create",
		"/TN", userTaskName,
		"/TR", `"`+target+`" run`,
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("registering the logon task failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func userUninstall() error {
	out, err := exec.Command("schtasks.exe", "/Delete", "/TN", userTaskName, "/F").CombinedOutput()
	if err != nil && !strings.Contains(string(out), "cannot find") {
		return fmt.Errorf("removing the logon task failed: %s", strings.TrimSpace(string(out)))
	}
	if p := userInstallPath(); p != "" {
		_ = os.Remove(p)
	}
	return nil
}

func userStart() error {
	out, err := exec.Command("schtasks.exe", "/Run", "/TN", userTaskName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("starting the agent failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func userStop() error {
	out, err := exec.Command("schtasks.exe", "/End", "/TN", userTaskName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("stopping the agent failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// userInstalled reports whether the logon task exists, which is what
// distinguishes a per-user install from a machine one.
func userInstalled() bool {
	err := exec.Command("schtasks.exe", "/Query", "/TN", userTaskName).Run()
	return err == nil
}
