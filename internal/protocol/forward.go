package protocol

import (
	"bufio"
	"errors"
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

	// ServiceDev reaches the ssh server embedded in the agent, rather than a
	// port on the target. Reserved: it is answered by the agent itself and can
	// never be pointed at an address, which is what keeps it from becoming a
	// route into the network behind the machine.
	//
	// Separate from ServiceSSH on purpose. That one forwards to the target's
	// own sshd, with the target's accounts and keys; this one is the agent
	// answering, and needs neither. Silently substituting one for the other
	// would change who authenticates without saying so.
	ServiceDev = "dev"
)

// WriteForwardTarget names the service on a freshly opened forward stream.
func WriteForwardTarget(w io.Writer, service string) error {
	if strings.ContainsAny(service, "\r\n") {
		return fmt.Errorf("service name must be one line: %q", service)
	}
	_, err := io.WriteString(w, service+"\n")
	return err
}

// A forward stream carries one status line back before it turns into a raw
// pipe. Without it a refusal is indistinguishable from a service that accepted
// and hung up, and the operator is left guessing at the other end of a tunnel
// they cannot see into.
const (
	forwardOK       = "ok"
	forwardMaxError = 512
)

// WriteForwardStatus reports whether the agent reached the service.
func WriteForwardStatus(w io.Writer, cause error) error {
	if cause == nil {
		_, err := io.WriteString(w, forwardOK+"\n")
		return err
	}
	msg := strings.ReplaceAll(cause.Error(), "\n", " ")
	if len(msg) > forwardMaxError {
		msg = msg[:forwardMaxError]
	}
	_, err := io.WriteString(w, "error "+msg+"\n")
	return err
}

// ReadForwardStatus reads the line written by WriteForwardStatus. Anything
// buffered past it belongs to the connection and must be preserved by the
// caller, which is what WithBuffered is for.
func ReadForwardStatus(r *bufio.Reader) error {
	line, err := r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("the agent closed the stream without answering: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == forwardOK {
		return nil
	}
	return errors.New(strings.TrimSpace(strings.TrimPrefix(line, "error")))
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
