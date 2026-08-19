package main

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// upperEchoServer stands in for a local service. It answers in upper case so a
// test can tell which direction a byte travelled.
func upperEchoServer(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 256)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						conn.Write([]byte(strings.ToUpper(string(buf[:n]))))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func withService(t *testing.T, name, addr string) {
	t.Helper()
	previous := services
	services = map[string]string{name: addr}
	t.Cleanup(func() { services = previous })
}

// The target line and the first bytes of the connection routinely arrive in
// the same packet. Reading the socket instead of the buffered reader would
// swallow those bytes, so the test writes them together on purpose.
func TestForwardCarriesBytesArrivingWithTheTargetLine(t *testing.T) {
	withService(t, "svc", upperEchoServer(t))

	relay, agent := net.Pipe()
	defer relay.Close()
	go func() { defer agent.Close(); handleForward(agent, bufio.NewReader(agent)) }()

	relay.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := relay.Write([]byte("svc\nhello")); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, 5)
	if _, err := io.ReadFull(relay, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "HELLO" {
		t.Fatalf("got %q, want %q", got, "HELLO")
	}
}

func TestForwardCarriesBytesSentAfterTheTargetLine(t *testing.T) {
	withService(t, "svc", upperEchoServer(t))

	relay, agent := net.Pipe()
	defer relay.Close()
	go func() { defer agent.Close(); handleForward(agent, bufio.NewReader(agent)) }()

	relay.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := relay.Write([]byte("svc\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, 6)
	if _, err := io.ReadFull(relay, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "SECOND" {
		t.Fatalf("got %q, want %q", got, "SECOND")
	}
}

// An agent must not dial anything it was not configured to offer, otherwise it
// is a route into the network behind it.
func TestForwardRefusesAServiceItDoesNotOffer(t *testing.T) {
	withService(t, "svc", upperEchoServer(t))

	relay, agent := net.Pipe()
	defer relay.Close()
	go func() { defer agent.Close(); handleForward(agent, bufio.NewReader(agent)) }()

	relay.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := relay.Write([]byte("something-else\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Read(make([]byte, 1)); err == nil {
		t.Fatal("the stream should have been closed, not answered")
	}
}

func TestSetServicesMergesOverDefaultsAndCanWithdraw(t *testing.T) {
	previous := services
	t.Cleanup(func() { services = previous })

	setServices(map[string]string{"vnc": "127.0.0.1:5900"})
	if services[protocol.ServiceRDP] != "127.0.0.1:3389" {
		t.Fatalf("the rdp default should survive, got %q", services[protocol.ServiceRDP])
	}
	if services[protocol.ServiceSSH] != "127.0.0.1:22" {
		t.Fatalf("the ssh default should survive, got %q", services[protocol.ServiceSSH])
	}
	if services["vnc"] != "127.0.0.1:5900" {
		t.Fatal("a configured service should be added")
	}

	setServices(map[string]string{protocol.ServiceRDP: ""})
	if _, ok := services[protocol.ServiceRDP]; ok {
		t.Fatal("an empty address should withdraw a service")
	}
}
