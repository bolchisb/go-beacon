//go:build !windows

package main

// WSL is a Windows feature. Reporting "absent" here rather than omitting the
// field keeps the dashboard's answer the same shape on every platform.
func detectWSL() wslState {
	return wslState{Status: "absent", Detail: "WSL exists only on Windows"}
}

func wslShell(string) (string, []string) { return "", nil }
