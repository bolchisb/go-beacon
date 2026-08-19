package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/atotto/clipboard"
	"github.com/bolchisb/go-beacon/internal/protocol"
)

// A headless linux host has no clipboard at all. That is a missing capability,
// not a failure, and the message has to say what would fix it.
func TestClipboardReportsWhyItIsUnavailable(t *testing.T) {
	if !clipboard.Unsupported {
		t.Skip("this host has a clipboard")
	}

	for _, resp := range []protocol.RPCResponse{
		opClipboardRead(),
		opClipboardWrite(protocol.RPCRequest{Content: base64.StdEncoding.EncodeToString([]byte("x"))}),
	} {
		if resp.Error == "" {
			t.Fatal("expected an error explaining the missing clipboard")
		}
		for _, hint := range []string{"xclip", "xsel", "wl-clipboard"} {
			if !strings.Contains(resp.Error, hint) {
				t.Fatalf("error %q should name %s as a fix", resp.Error, hint)
			}
		}
	}
}

func TestClipboardRoundTrip(t *testing.T) {
	if clipboard.Unsupported {
		t.Skip("no clipboard on this host")
	}
	t.Cleanup(func() { clipboard.WriteAll("") })

	const want = "sentinel value ăâîșț"
	if resp := opClipboardWrite(protocol.RPCRequest{
		Content: base64.StdEncoding.EncodeToString([]byte(want)),
	}); resp.Error != "" {
		t.Fatalf("write failed: %s", resp.Error)
	}

	resp := opClipboardRead()
	if resp.Error != "" {
		t.Fatalf("read failed: %s", resp.Error)
	}
	got, err := base64.StdEncoding.DecodeString(resp.Content)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestClipboardWriteRejectsBadBase64(t *testing.T) {
	resp := opClipboardWrite(protocol.RPCRequest{Content: "not base64!!"})
	if resp.Error == "" {
		t.Fatal("expected an error")
	}
}
