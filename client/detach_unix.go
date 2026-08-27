//go:build !windows

package main

import "syscall"

// detachedProcess starts the helper in its own session, so it survives the
// service being stopped moments later. Without this, stopping the service takes
// the helper with it and the restart never happens.
func detachedProcess() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
