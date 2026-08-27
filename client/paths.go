package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// The agent normally runs as a service, so its files live in system-wide
// locations. Every path can be overridden by an environment variable, which is
// what makes an unprivileged foreground run possible during development.

// configPath finds this agent's config.
//
// On Windows there are two possible homes, because an agent can be installed
// two ways: as a machine-wide service, or per user with no elevation at all.
// The per-user file wins when it exists, so a user install shadows a machine
// one for that user rather than fighting it. Nothing has to be told which mode
// it is in.
func configPath() string {
	if p := os.Getenv("BEACON_CONFIG"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		if user := userConfigPath(); user != "" {
			if _, err := os.Stat(user); err == nil {
				return user
			}
		}
		return filepath.Join(programData(), "beacon", "config.json")
	}
	return "/etc/beacon/config.json"
}

// userConfigPath is where a per-user install keeps its config. Empty when the
// platform has no such notion.
func userConfigPath() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	dir := os.Getenv("APPDATA")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "beacon", "config.json")
}

// userInstallPath is where a per-user install puts the binary: under the
// user's own profile, which needs no elevation to write.
func userInstallPath() string {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "beacon", "beacon.exe")
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
