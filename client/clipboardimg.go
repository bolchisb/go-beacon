package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// Putting an image on the machine's clipboard.
//
// The text clipboard goes through a Go library. This one cannot: the library is
// text-only, and the ones that do handle images need cgo on Linux and macOS.
// This project builds six platforms from one machine with CGO_ENABLED=0, which
// is worth more than avoiding a subprocess -- so each platform's own tool does
// the work, and the agent stays a static binary.
//
// The image reaches those tools as a file rather than on stdin, because that is
// the one calling convention all three accept.

// pngMagic identifies a PNG. The check is not about trust -- the caller already
// came through the relay's gate -- but about the error: a platform tool handed
// something that is not an image fails in its own words, and those words are
// rarely about the clipboard.
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func opClipboardWriteImage(req protocol.RPCRequest) protocol.RPCResponse {
	raw, err := base64.StdEncoding.DecodeString(req.Content)
	if err != nil {
		return protocol.RPCResponse{Error: "content must be base64: " + err.Error()}
	}
	if len(raw) == 0 {
		return protocol.RPCResponse{Error: "no image was sent"}
	}
	if len(raw) > protocol.MaxClipboardImageBytes {
		return protocol.RPCResponse{Error: fmt.Sprintf(
			"image is %d bytes, over the %d byte limit", len(raw), protocol.MaxClipboardImageBytes)}
	}
	if !bytes.HasPrefix(raw, pngMagic) {
		return protocol.RPCResponse{Error: "only PNG images are accepted"}
	}

	path, err := writeTempPNG(raw)
	if err != nil {
		return protocol.RPCResponse{Error: "cannot stage the image: " + err.Error()}
	}
	// The tools read the file while they run; none of them keep it. Removing it
	// afterwards matters more than usual here, because a screenshot pasted into
	// a terminal is often the thing somebody did not want lying around.
	defer os.Remove(path)

	if err := setClipboardImage(path); err != nil {
		return protocol.RPCResponse{Error: err.Error()}
	}
	return protocol.RPCResponse{Written: len(raw)}
}

// firstLine keeps an error from a subprocess to something a dashboard can show.
// The tools are talkative on failure and the first line is the part that names
// what went wrong.
func firstLine(out string, fallback error) string {
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return fallback.Error()
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// tempPNGPath reserves a path for a platform tool to write to. The file is
// created and closed rather than only named: the tools want somewhere that
// exists, and creating it here is what keeps it at this process's umask.
func tempPNGPath() (string, error) {
	f, err := os.CreateTemp("", "beacon-grab-*.png")
	if err != nil {
		return "", err
	}
	path := filepath.Clean(f.Name())
	return path, f.Close()
}

func writeTempPNG(raw []byte) (string, error) {
	f, err := os.CreateTemp("", "beacon-paste-*.png")
	if err != nil {
		return "", err
	}
	path := filepath.Clean(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func opClipboardReadImage() protocol.RPCResponse {
	raw, err := clipboardImage()
	if errors.Is(err, errNoClipboardImage) {
		return protocol.RPCResponse{Error: protocol.ClipboardNoImage}
	}
	if err != nil {
		return protocol.RPCResponse{Error: err.Error()}
	}
	if !bytes.HasPrefix(raw, pngMagic) {
		return protocol.RPCResponse{Error: "the clipboard tool returned something that is not a PNG"}
	}

	truncated := false
	if len(raw) > protocol.MaxClipboardImageBytes {
		raw, truncated = raw[:protocol.MaxClipboardImageBytes], true
	}
	return protocol.RPCResponse{
		Content:   base64.StdEncoding.EncodeToString(raw),
		Truncated: truncated,
	}
}

// errNoClipboardImage separates "there is no image on the clipboard", which is
// what a clipboard holding text looks like and is not a failure, from a host
// that has no clipboard to read at all.
var errNoClipboardImage = errors.New("no image on the clipboard")
