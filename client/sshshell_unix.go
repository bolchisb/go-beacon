//go:build !windows

package main

import "os"

// interactiveShell is what an operator lands in with no command given. A login
// shell, so the machine's own profile applies -- PATH, aliases, the toolchain
// a developer expects to find.
func interactiveShell() (string, []string) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return shell, []string{"-l"}
}

// commandShell runs one command. sh -c rather than splitting the string
// ourselves: the client sent shell syntax and expects shell semantics.
func commandShell(command string) (string, []string) {
	return "/bin/sh", []string{"-c", command}
}
