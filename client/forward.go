package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// The dial is bounded; the connection itself is not. A remote desktop session
// lasts as long as someone is working in it.
const forwardDialTimeout = 10 * time.Second

// services maps a service name to the local address the agent will dial for
// it. It is written once, before any tunnel exists, and only read afterwards.
var services = defaultServices()

// defaultServices is what an agent offers when its config says nothing. Only
// loopback: forwarding is meant to reach this machine, not to turn the agent
// into a route into the network behind it.
func defaultServices() map[string]string {
	return map[string]string{
		protocol.ServiceRDP: "127.0.0.1:3389",
		// ssh is on by default because it widens nothing: the relay already
		// hands out a terminal on this machine, and that one asks for no
		// credentials at all. Forwarding to sshd is the stricter of the two.
		protocol.ServiceSSH: "127.0.0.1:22",
	}
}

// setServices merges the operator's config over the defaults.
func setServices(configured map[string]string) {
	merged := defaultServices()
	for name, addr := range configured {
		if addr == "" {
			delete(merged, name) // an empty address withdraws a service
			continue
		}
		merged[name] = addr
	}
	services = merged
}

// handleForward carries one raw connection to a local service. The agent never
// looks at what flows through: remote desktop speaks its own protocol and this
// is a pipe.
func handleForward(stream net.Conn, br *bufio.Reader) {
	service, err := protocol.ReadForwardTarget(br)
	if err != nil {
		slog.Warn("forward: unreadable target", "err", err)
		return
	}

	addr, ok := services[service]
	if !ok {
		slog.Warn("forward: service not offered", "service", service)
		protocol.WriteForwardStatus(stream, fmt.Errorf("this agent does not offer %q", service))
		return
	}

	local, err := net.DialTimeout("tcp", addr, forwardDialTimeout)
	if err != nil {
		slog.Warn("forward: cannot reach the local service", "service", service, "addr", addr, "err", err)
		// say why, or the operator sees a session that opened and closed
		protocol.WriteForwardStatus(stream, fmt.Errorf("%s is not answering on %s: %w", service, addr, err))
		return
	}
	if err := protocol.WriteForwardStatus(stream, nil); err != nil {
		local.Close()
		return
	}

	slog.Info("forward: session open", "service", service, "addr", addr)
	pipeStream(stream, br, local)
	slog.Info("forward: session closed", "service", service)
}

// pipeStream copies in both directions and returns when either end is done.
// Reads come from br, not from stream: the target line arrived in the same
// packet as the first bytes of the connection often enough that reading the
// socket directly would lose them.
func pipeStream(stream net.Conn, br *bufio.Reader, local net.Conn) {
	var once sync.Once
	shut := func() {
		once.Do(func() {
			stream.Close()
			local.Close()
		})
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); defer shut(); io.Copy(local, br) }()
	go func() { defer wg.Done(); defer shut(); io.Copy(stream, local) }()
	wg.Wait()
}
