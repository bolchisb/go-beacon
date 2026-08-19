package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// A terminal stream is asymmetric on purpose. The relay has two things to say
// -- keystrokes and window sizes -- so its direction is framed. The agent only
// ever sends terminal output, so it writes raw bytes and the relay can shovel
// them onward without parsing anything.
const (
	PTYData   byte = 0
	PTYResize byte = 1
)

// maxPTYFrame bounds a single frame so a confused peer cannot make the other
// side allocate without limit.
const maxPTYFrame = 1 << 20

// WritePTYFrame writes one relay-to-agent frame: type, big-endian length, payload.
func WritePTYFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > maxPTYFrame {
		return fmt.Errorf("pty frame too large: %d bytes", len(payload))
	}
	hdr := make([]byte, 5)
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadPTYFrame reads one frame written by WritePTYFrame.
func ReadPTYFrame(r io.Reader) (byte, []byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxPTYFrame {
		return 0, nil, fmt.Errorf("pty frame too large: %d bytes", n)
	}
	if n == 0 {
		return hdr[0], nil, nil
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

// PTYResizePayload encodes a window size for a PTYResize frame.
func PTYResizePayload(cols, rows uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:], cols)
	binary.BigEndian.PutUint16(b[2:], rows)
	return b
}

// PTYSize decodes the payload of a PTYResize frame.
func PTYSize(payload []byte) (cols, rows uint16, err error) {
	if len(payload) != 4 {
		return 0, 0, errors.New("resize payload must be 4 bytes")
	}
	return binary.BigEndian.Uint16(payload[0:]), binary.BigEndian.Uint16(payload[2:]), nil
}
