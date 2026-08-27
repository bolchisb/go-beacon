//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// setClipboardImage goes through PowerShell, which is the only image-capable
// clipboard on Windows reachable without cgo.
//
// -STA is not optional. The Windows clipboard is a single-threaded apartment
// API, and PowerShell's default multi-threaded apartment makes SetImage fail
// with a message about threading that says nothing about clipboards.
func setClipboardImage(path string) error {
	script := `Add-Type -AssemblyName System.Windows.Forms,System.Drawing; ` +
		`$img = [System.Drawing.Image]::FromFile('` + escapePS(path) + `'); ` +
		`try { [System.Windows.Forms.Clipboard]::SetImage($img) } finally { $img.Dispose() }`

	for _, shell := range []string{"pwsh.exe", "powershell.exe"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		out, err := exec.Command(shell, "-NoProfile", "-STA", "-Command", script).CombinedOutput()
		if err == nil {
			return nil
		}
		return fmt.Errorf("could not set the clipboard image: %s", firstLine(string(out), err))
	}
	return fmt.Errorf("no powershell on this machine, so an image cannot be placed on its clipboard")
}

// escapePS doubles single quotes, which is how a literal one is written inside
// a PowerShell single-quoted string. Temp paths are ours, but a user profile
// name is not.
func escapePS(s string) string { return strings.ReplaceAll(s, "'", "''") }

// clipboardImage reads an image off the Windows clipboard.
//
// GetImage returns nothing when the clipboard holds text, which is the common
// case and not a failure, so the script reports that as its own exit code
// rather than leaving it to be guessed from an empty file.
func clipboardImage() ([]byte, error) {
	path, err := tempPNGPath()
	if err != nil {
		return nil, err
	}
	defer os.Remove(path)

	script := `Add-Type -AssemblyName System.Windows.Forms,System.Drawing; ` +
		`$img = [System.Windows.Forms.Clipboard]::GetImage(); ` +
		`if ($img -eq $null) { exit 3 }; ` +
		`try { $img.Save('` + escapePS(path) + `', [System.Drawing.Imaging.ImageFormat]::Png) } ` +
		`finally { $img.Dispose() }`

	for _, shell := range []string{"pwsh.exe", "powershell.exe"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		cmd := exec.Command(shell, "-NoProfile", "-STA", "-Command", script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			if code := cmd.ProcessState.ExitCode(); code == 3 {
				return nil, errNoClipboardImage
			}
			return nil, fmt.Errorf("could not read the clipboard image: %s", firstLine(string(out), err))
		}
		return os.ReadFile(path)
	}
	return nil, fmt.Errorf("no powershell on this machine, so its clipboard cannot be read for an image")
}
