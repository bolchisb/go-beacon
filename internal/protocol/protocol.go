// Package protocol defines the wire contract shared by the beacon server and
// the agent: how an agent announces itself over the HTTP upgrade handshake and
// how each multiplexed stream declares its purpose.
package protocol

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
)

const (
	// ConnectPath is the HTTP endpoint an agent upgrades on.
	ConnectPath = "/agent/connect"

	// UpgradeProto is the value of the Upgrade header on both sides.
	UpgradeProto = "beacon/1"

	HeaderAgentID  = "X-Beacon-Agent-Id"
	HeaderHostname = "X-Beacon-Hostname"
	HeaderOS       = "X-Beacon-Os"
	HeaderArch     = "X-Beacon-Arch"
	HeaderVersion  = "X-Beacon-Version"
)

// Hello is the identity an agent presents when it connects. Today it travels
// in HTTP headers; once mTLS lands the AgentID is taken from the client
// certificate instead and the remaining fields stay descriptive only.
type Hello struct {
	AgentID  string `json:"agent_id"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
}

// SetHeaders writes the hello onto an outgoing request.
func (h Hello) SetHeaders(hdr http.Header) {
	hdr.Set(HeaderAgentID, h.AgentID)
	hdr.Set(HeaderHostname, h.Hostname)
	hdr.Set(HeaderOS, h.OS)
	hdr.Set(HeaderArch, h.Arch)
	hdr.Set(HeaderVersion, h.Version)
}

// HelloFromHeaders reads the hello off an incoming request.
func HelloFromHeaders(hdr http.Header) (Hello, error) {
	h := Hello{
		AgentID:  strings.TrimSpace(hdr.Get(HeaderAgentID)),
		Hostname: strings.TrimSpace(hdr.Get(HeaderHostname)),
		OS:       strings.TrimSpace(hdr.Get(HeaderOS)),
		Arch:     strings.TrimSpace(hdr.Get(HeaderArch)),
		Version:  strings.TrimSpace(hdr.Get(HeaderVersion)),
	}
	if h.AgentID == "" {
		return Hello{}, errors.New("missing agent id")
	}
	if len(h.AgentID) > 128 {
		return Hello{}, errors.New("agent id too long")
	}
	return h, nil
}

// WithBuffered returns a connection that first yields whatever the reader has
// already pulled off the wire. Both sides of the upgrade need this: the peer
// may legitimately start the tunnel in the same TCP segment as the handshake
// response, and those bytes belong to yamux, not to the HTTP parser that
// happened to buffer them.
func WithBuffered(conn net.Conn, br *bufio.Reader) net.Conn {
	n := br.Buffered()
	if n == 0 {
		return conn
	}
	return &prefixConn{Conn: conn, r: io.MultiReader(io.LimitReader(br, int64(n)), conn)}
}

type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// CountingConn tracks how many bytes crossed the tunnel in each direction, so
// both ends can report real traffic instead of just "the socket is open".
type CountingConn struct {
	net.Conn
	in  atomic.Uint64
	out atomic.Uint64
}

func NewCountingConn(c net.Conn) *CountingConn { return &CountingConn{Conn: c} }

func (c *CountingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.in.Add(uint64(n))
	return n, err
}

func (c *CountingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.out.Add(uint64(n))
	return n, err
}

func (c *CountingConn) In() uint64  { return c.in.Load() }
func (c *CountingConn) Out() uint64 { return c.out.Load() }

// Stream kinds. Each yamux stream opens with one of these, newline terminated.
const (
	StreamEcho = "echo"
)

// WriteStreamHeader declares what a freshly opened stream is for.
func WriteStreamHeader(w io.Writer, kind string) error {
	_, err := io.WriteString(w, kind+"\n")
	return err
}

// ReadStreamHeader reads the declaration written by WriteStreamHeader.
func ReadStreamHeader(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	kind := strings.TrimSpace(line)
	if kind == "" {
		return "", fmt.Errorf("empty stream header")
	}
	return kind, nil
}
