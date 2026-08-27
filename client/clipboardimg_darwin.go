//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// setClipboardImage goes through osascript. pbcopy is text-only; AppleScript is
// what can name a PNG as a clipboard type without linking against Cocoa.
func setClipboardImage(path string) error {
	script := fmt.Sprintf(
		`set the clipboard to (read (POSIX file %q) as «class PNGf»)`, path)
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("could not set the clipboard image: %s", firstLine(string(out), err))
	}
	return nil
}

// clipboardImage reads a PNG off the macOS clipboard.
//
// Asking for the clipboard as PNGf raises an AppleScript error when it holds
// something else, so the script catches that and answers in a word rather than
// failing: a clipboard full of text is not a fault.
func clipboardImage() ([]byte, error) {
	path, err := tempPNGPath()
	if err != nil {
		return nil, err
	}
	defer os.Remove(path)

	script := fmt.Sprintf(`try
	set thedata to (the clipboard as «class PNGf»)
on error
	return "none"
end try
set f to open for access POSIX file %q with write permission
set eof f to 0
write thedata to f
close access f
return "ok"`, path)

	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("could not read the clipboard image: %s", firstLine(string(out), err))
	}
	if strings.TrimSpace(string(out)) == "none" {
		return nil, errNoClipboardImage
	}
	return os.ReadFile(path)
}
