//go:build !windows && !darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// setClipboardImage uses whichever clipboard tool the session has, in the order
// that matches the session type: wl-copy under Wayland, xclip or xsel under X.
//
// These are the same tools the text clipboard already asks for, so a host that
// can copy text can copy an image and one that cannot says so the same way.
func setClipboardImage(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	candidates := []struct {
		name string
		args []string
	}{
		{"wl-copy", []string{"--type", "image/png"}},
		{"xclip", []string{"-selection", "clipboard", "-t", "image/png"}},
		{"xsel", []string{"--clipboard", "--input"}},
	}

	tried := false
	for _, c := range candidates {
		if _, err := exec.LookPath(c.name); err != nil {
			continue
		}
		tried = true
		cmd := exec.Command(c.name, c.args...)
		cmd.Stdin = bytesReader(raw)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		return fmt.Errorf("%s could not set the clipboard image: %s", c.name, firstLine(string(out), err))
	}
	if !tried {
		return fmt.Errorf("no clipboard tool on this host: an image needs one of " +
			"wl-clipboard, xclip or xsel, and a graphical session to talk to")
	}
	return nil
}

// clipboardImage reads a PNG off the clipboard, through whichever tool the
// session has. xsel is absent here on purpose: it has no notion of clipboard
// types, so it cannot be asked for an image.
func clipboardImage() ([]byte, error) {
	readers := []struct {
		name string
		args []string
	}{
		{"wl-paste", []string{"--type", "image/png", "--no-newline"}},
		{"xclip", []string{"-selection", "clipboard", "-t", "image/png", "-o"}},
	}

	tried := false
	for _, c := range readers {
		if _, err := exec.LookPath(c.name); err != nil {
			continue
		}
		tried = true
		out, err := exec.Command(c.name, c.args...).Output()
		if err != nil {
			// Both tools fail rather than answer empty when the clipboard holds
			// no image of that type, which is the ordinary case.
			return nil, errNoClipboardImage
		}
		if len(out) == 0 {
			return nil, errNoClipboardImage
		}
		return out, nil
	}
	if !tried {
		return nil, fmt.Errorf("no clipboard tool on this host that can read an image: " +
			"it needs wl-clipboard or xclip, and a graphical session to talk to")
	}
	return nil, errNoClipboardImage
}
