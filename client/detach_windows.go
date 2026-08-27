//go:build windows

package main

import "syscall"

const (
	detachedProcessFlag = 0x00000008
	createNewProcessGrp = 0x00000200
	createNoWindowFlag  = 0x08000000
)

// detachedProcess starts the helper outside the service's process group and
// without a console, so stopping the service does not take it down with it.
func detachedProcess() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: detachedProcessFlag | createNewProcessGrp | createNoWindowFlag,
	}
}
