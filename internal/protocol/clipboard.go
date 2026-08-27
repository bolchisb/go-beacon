package protocol

// Clipboard operations carried on a StreamRPC stream. Read answers through the
// Content field of RPCResponse and Write takes the same field of RPCRequest,
// both base64, so text that is not valid UTF-8 survives the trip.
const (
	OpClipboardRead  = "clipboard_read"
	OpClipboardWrite = "clipboard_write"
)
