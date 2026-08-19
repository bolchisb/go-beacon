package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// The agent normally runs as a service, so its files live in system-wide
// locations. Every path can be overridden by an environment variable, which is
// what makes an unprivileged foreground run possible during development.

func configPath() string {
	if p := os.Getenv("BEACON_CONFIG"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(programData(), "beacon", "config.json")
	}
	return "/etc/beacon/config.json"
}

// socketPaths lists candidate control socket locations, most privileged first.
// The agent listens on the first one it can create; status tries each in turn.
// The user-level fallback is what lets a developer run the agent and query it
// without root.
func socketPaths() []string {
	if p := os.Getenv("BEACON_SOCKET"); p != "" {
		return []string{p}
	}

	var out []string
	switch runtime.GOOS {
	case "windows":
		out = append(out, filepath.Join(programData(), "beacon", "control.sock"))
	case "darwin":
		out = append(out, "/var/run/beacon.sock")
	default:
		out = append(out, "/run/beacon.sock")
	}
	if dir, err := os.UserConfigDir(); err == nil {
		out = append(out, filepath.Join(dir, "beacon", "control.sock"))
	}
	return out
}

// installPath is where install copies the binary. A service must not point at
// whatever directory the operator happened to run the installer from.
func installPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(programFiles(), "beacon", "beacon.exe")
	}
	return "/usr/local/bin/beacon"
}

func programData() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}

func programFiles() string {
	if d := os.Getenv("ProgramFiles"); d != "" {
		return d
	}
	return `C:\Program Files`
}
