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
// Two possible homes on every platform, because there are two kinds of user.
// An installed agent runs as a service and keeps its config machine-wide, root
// owned and unreadable to anyone else -- it holds that machine's identity. A
// person running `beacon ssh` or `beacon forward` on their own laptop is not
// that, and must not need root to read a file about their own session.
//
// The per-user file wins when it exists, so a workstation config shadows a
// machine one for that user rather than fighting it, and nothing has to be told
// which case it is in.
func configPath() string {
	if p := os.Getenv("BEACON_CONFIG"); p != "" {
		return p
	}
	if user := userConfigPath(); user != "" {
		if _, err := os.Stat(user); err == nil {
			return user
		}
	}
	return machineConfigPath()
}

func machineConfigPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(programData(), "beacon", "config.json")
	}
	return "/etc/beacon/config.json"
}

// userConfigPath is the per-user config. On Windows it is where a --user
// install lives; everywhere else it is what a workstation uses, so that
// ProxyCommand -- which ssh runs as you, not as root -- can read it.
func userConfigPath() string {
	if runtime.GOOS == "windows" {
		dir := os.Getenv("APPDATA")
		if dir == "" {
			return ""
		}
		return filepath.Join(dir, "beacon", "config.json")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
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
