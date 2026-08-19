package protocol

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// StreamForward carries one raw TCP connection to a service the agent hosts
// locally, such as remote desktop. After the stream header comes one more
// newline-terminated line naming the service; everything after that is the
// connection itself, untouched in both directions.
//
// The name is a service, never a host and port. If the caller could name an
// address, every agent would become an open proxy into the network it sits in.
// Naming a service leaves the agent in charge of what it is willing to expose.
const StreamForward = "forward"

// Service names understood by the agent. The port each maps to is the agent's
// own configuration, not the caller's business.
const (
	ServiceRDP = "rdp"
	ServiceSSH = "ssh"
)

// WriteForwardTarget names the service on a freshly opened forward stream.
func WriteForwardTarget(w io.Writer, service string) error {
	if strings.ContainsAny(service, "\r\n") {
		return fmt.Errorf("service name must be one line: %q", service)
	}
	_, err := io.WriteString(w, service+"\n")
	return err
}

// ReadForwardTarget reads the service name written by WriteForwardTarget.
func ReadForwardTarget(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	service := strings.TrimSpace(line)
	if service == "" {
		return "", fmt.Errorf("empty service name")
	}
	return service, nil
}
