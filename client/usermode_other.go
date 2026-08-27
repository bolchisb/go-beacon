//go:build !windows

package main

import "fmt"

// Per-user install is a Windows notion. On unix the service managers already
// have user-scoped modes (systemd --user, launchd LaunchAgents), and wiring
// those up is a separate piece of work rather than this one wearing a disguise.

func userInstall(string) error { return fmt.Errorf("a per-user install is only available on Windows") }
func userUninstall() error     { return fmt.Errorf("a per-user install is only available on Windows") }
func userStart() error         { return fmt.Errorf("a per-user install is only available on Windows") }
func userStop() error          { return fmt.Errorf("a per-user install is only available on Windows") }
func userInstalled() bool      { return false }
