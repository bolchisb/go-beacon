//go:build windows

package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// WSL detection.
//
// Reporting rather than forcing, because the blocker is not in this code. WSL
// distributions are registered per user, under HKEY_CURRENT_USER with the disk
// images in that user's profile. The agent normally runs as a service under
// LocalSystem, which has no user hive to read, so `wsl.exe -l` from there finds
// nothing or is refused outright.
//
// So the answer depends on how the agent was started, and the honest thing is
// to say which case this machine is in rather than to fail obscurely at the
// moment somebody asks for a shell.
func detectWSL() wslState {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// -l -q lists installed distributions, one per line, nothing else.
	out, err := exec.CommandContext(ctx, "wsl.exe", "-l", "-q").Output()
	if err != nil {
		if ctx.Err() != nil {
			return wslState{Status: "unavailable", Detail: "wsl.exe did not answer within five seconds"}
		}
		if _, ok := err.(*exec.Error); ok {
			return wslState{Status: "absent", Detail: "wsl.exe is not installed on this machine"}
		}
		// An exit status here is usually LocalSystem being refused, or a user
		// hive with nothing registered in it.
		return wslState{
			Status: "unreachable",
			Detail: "wsl.exe refused this context. WSL distributions belong to a " +
				"user account, and this agent is most likely running as a service " +
				"under LocalSystem, which has none.",
		}
	}

	// wsl.exe writes UTF-16; the nulls have to go before the names are readable.
	text := strings.ReplaceAll(string(out), "\x00", "")
	var distros []string
	for _, line := range strings.Split(text, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			distros = append(distros, name)
		}
	}
	if len(distros) == 0 {
		return wslState{
			Status: "unreachable",
			Detail: "wsl.exe answered but listed no distributions, which is what " +
				"a service account sees: they are registered per user.",
		}
	}
	return wslState{Status: "available", Distros: distros}
}

// wslShell is what to run for a WSL session. The default distribution unless
// one is named.
func wslShell(distro string) (string, []string) {
	if distro == "" {
		return "wsl.exe", nil
	}
	return "wsl.exe", []string{"-d", distro}
}
