//go:build windows

package main

// interactiveShell prefers PowerShell, which is what an operator on a Windows
// host expects to land in, and falls back to the command processor.
func interactiveShell() (string, []string) {
	return windowsShell(), nil
}

// commandShell runs one command through the command processor. cmd rather than
// PowerShell on purpose: scp and rsync send POSIX-ish command lines, and
// PowerShell's own parsing mangles them.
func commandShell(command string) (string, []string) {
	return "cmd.exe", []string{"/c", command}
}
