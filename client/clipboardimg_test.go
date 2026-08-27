package main

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// smallestPNG is a real one-pixel image: the magic, an IHDR, an IDAT and an
// IEND. A handful of bytes that happen to start with the magic would pass the
// sniff and then fail in a platform tool, which is not what these check.
var smallestPNG = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
	0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
	0x0d, 0x0a, 0x2d, 0xb4,
	0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

func TestClipboardImageRefusesWhatIsNotAPNG(t *testing.T) {
	resp := opClipboardWriteImage(protocol.RPCRequest{
		Content: base64.StdEncoding.EncodeToString([]byte("GIF89a and then some")),
	})
	if !strings.Contains(resp.Error, "PNG") {
		t.Fatalf("expected a refusal naming PNG, got %q", resp.Error)
	}
}

func TestClipboardImageRefusesAnEmptyOrOversizedPaste(t *testing.T) {
	if resp := (opClipboardWriteImage(protocol.RPCRequest{Content: ""})); resp.Error == "" {
		t.Fatal("an empty paste should be refused")
	}

	huge := make([]byte, protocol.MaxClipboardImageBytes+1)
	copy(huge, pngMagic)
	resp := opClipboardWriteImage(protocol.RPCRequest{
		Content: base64.StdEncoding.EncodeToString(huge),
	})
	if !strings.Contains(resp.Error, "limit") {
		t.Fatalf("expected a refusal naming the limit, got %q", resp.Error)
	}
}

func TestClipboardImageRefusesContentThatIsNotBase64(t *testing.T) {
	resp := opClipboardWriteImage(protocol.RPCRequest{Content: "not base64 at all!!"})
	if !strings.Contains(resp.Error, "base64") {
		t.Fatalf("expected a refusal naming base64, got %q", resp.Error)
	}
}

// TestStagedImageIsRemoved covers the part that is easy to leave behind: a
// screenshot pasted into a terminal is often exactly what should not be sitting
// in a temp directory afterwards.
func TestStagedImageIsRemoved(t *testing.T) {
	path, err := writeTempPNG(smallestPNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the staged file should exist: %v", err)
	}
	os.Remove(path)

	// The operation stages and removes in one call. It will fail on a host with
	// no clipboard, which is every CI machine, but the file must be gone either
	// way.
	before, _ := os.ReadDir(os.TempDir())
	_ = opClipboardWriteImage(protocol.RPCRequest{
		Content: base64.StdEncoding.EncodeToString(smallestPNG),
	})
	after, _ := os.ReadDir(os.TempDir())

	count := func(entries []os.DirEntry) int {
		n := 0
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "beacon-paste-") {
				n++
			}
		}
		return n
	}
	if count(after) > count(before) {
		t.Fatal("the staged image outlived the call")
	}
}

// TestReadingAnImageSaysWhenThereIsNone separates the two answers that matter
// on the read side. A clipboard holding text is the ordinary case and gets the
// exact sentence the relay matches on; a host with no clipboard tool at all is
// a different problem and must not be reported as an empty clipboard.
func TestReadingAnImageSaysWhenThereIsNone(t *testing.T) {
	resp := opClipboardReadImage()
	if resp.Error == "" {
		t.Skip("this host has a clipboard with an image on it")
	}
	if resp.Error != protocol.ClipboardNoImage &&
		!strings.Contains(resp.Error, "clipboard") {
		t.Fatalf("an unhelpful refusal: %q", resp.Error)
	}
}

// TestTheTwoRefusalsAreDistinguishable is what the relay depends on: it matches
// the empty-clipboard answer exactly to decide whether the assistant is told
// "there is no image" or shown an error.
func TestTheTwoRefusalsAreDistinguishable(t *testing.T) {
	if protocol.ClipboardNoImage == "" {
		t.Fatal("the sentinel must not be empty, or every failure would match it")
	}
	notAnImage := opClipboardWriteImage(protocol.RPCRequest{
		Content: base64.StdEncoding.EncodeToString([]byte("nope")),
	})
	if notAnImage.Error == protocol.ClipboardNoImage {
		t.Fatal("a write failure must not look like an empty clipboard")
	}
}
