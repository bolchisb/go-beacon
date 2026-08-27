package protocol

// Clipboard operations carried on a StreamRPC stream. Read answers through the
// Content field of RPCResponse and Write takes the same field of RPCRequest,
// both base64, so text that is not valid UTF-8 survives the trip.
const (
	OpClipboardRead  = "clipboard_read"
	OpClipboardWrite = "clipboard_write"

	// OpClipboardWriteImage puts a PNG on the machine's clipboard. It is a
	// separate operation rather than a flag on the write above because the two
	// have nothing in common past the name: text goes through a library, an
	// image goes through whatever the platform provides, and the size a caller
	// may send differs by an order of magnitude.
	OpClipboardWriteImage = "clipboard_write_image"

	// OpClipboardReadImage answers with a PNG from the machine's clipboard, or
	// says there is none. "None" is the ordinary case, not a fault: a clipboard
	// usually holds text.
	OpClipboardReadImage = "clipboard_read_image"
)

// ClipboardNoImage is the exact answer OpClipboardReadImage gives when the
// clipboard holds no image. A caller matches on it to tell an empty clipboard
// apart from a machine that cannot read one at all, which want different words
// and different fixes.
const ClipboardNoImage = "the clipboard holds no image"

// MaxClipboardImageBytes bounds one image. A screenshot of a 4K display in PNG
// lands comfortably under this; the cap is here so a tunnel cannot be filled by
// one paste.
const MaxClipboardImageBytes = 8 << 20 // 8 MiB
