package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTransportSeparatesWhatIsVerifiedFromWhatIsClaimed is the point of having
// three states rather than two. A forwarded header is written by whoever sent
// the request, so it must not come back looking the same as a handshake this
// relay performed itself.
func TestTransportSeparatesWhatIsVerifiedFromWhatIsClaimed(t *testing.T) {
	terminated := httptest.NewRequest(http.MethodGet, "/agent/connect", nil)
	terminated.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	got, detail := parseProxies("").transportOf(terminated)
	if got != TransportTLS {
		t.Fatalf("terminated here: got %q, want %q", got, TransportTLS)
	}
	if !strings.Contains(detail, "TLS 1.3") {
		t.Fatalf("the detail should name the version, got %q", detail)
	}

	// The same header an agent could set for itself, on a relay that has named
	// no proxy: reported as a claim, not as a fact.
	forwarded := httptest.NewRequest(http.MethodGet, "/agent/connect", nil)
	forwarded.Header.Set("X-Forwarded-Proto", "https")
	claimed, detail := parseProxies("").transportOf(forwarded)
	if claimed != TransportProxyTLS {
		t.Fatalf("forwarded: got %q, want %q", claimed, TransportProxyTLS)
	}
	if detail != "" {
		t.Fatalf("a claim carries nothing to report, got %q", detail)
	}

	plain := httptest.NewRequest(http.MethodGet, "/agent/connect", nil)
	if got, _ := parseProxies("").transportOf(plain); got != TransportPlain {
		t.Fatalf("plain: got %q, want %q", got, TransportPlain)
	}
}

// TestTransportIsNotReportedForADroppedSession keeps the dashboard from drawing
// a padlock next to "disconnected", where it would describe a connection that
// no longer exists.
func TestTransportIsNotReportedForADroppedSession(t *testing.T) {
	r := newRegistry()
	r.agents["target-01"] = &agentRec{
		transport:       TransportTLS,
		transportDetail: "this relay terminated the handshake, TLS 1.3",
		online:          false,
	}

	views := r.Snapshot()
	if len(views) != 1 {
		t.Fatalf("expected one agent, got %d", len(views))
	}
	if views[0].Transport != "" || views[0].TransportDetail != "" {
		t.Fatalf("an offline agent should report no transport, got %q/%q",
			views[0].Transport, views[0].TransportDetail)
	}
}

// TestAForwardedClaimIsBelievedOnlyFromTheProxy is the deployment this exists
// for: agents arrive through a terminating proxy on 443, and the relay's own
// port is also reachable for testing. Both send the same header if they choose
// to. Only the peer decides which is which.
func TestAForwardedClaimIsBelievedOnlyFromTheProxy(t *testing.T) {
	proxies := parseProxies("172.18.0.0/16, 10.0.0.7")

	viaProxy := httptest.NewRequest(http.MethodGet, "/agent/connect", nil)
	viaProxy.Header.Set("X-Forwarded-Proto", "https")
	viaProxy.RemoteAddr = "172.18.0.4:44012"
	if got, _ := proxies.transportOf(viaProxy); got != TransportTLS {
		t.Fatalf("through the proxy: got %q, want %q", got, TransportTLS)
	}

	// An agent on the LAN, talking to the relay's port directly, saying the
	// same thing about itself.
	direct := httptest.NewRequest(http.MethodGet, "/agent/connect", nil)
	direct.Header.Set("X-Forwarded-Proto", "https")
	direct.RemoteAddr = "192.168.100.31:51022"
	if got, _ := proxies.transportOf(direct); got != TransportPlain {
		t.Fatalf("a direct agent claiming https: got %q, want %q", got, TransportPlain)
	}

	// A single address, and one next to it that was not named.
	named := httptest.NewRequest(http.MethodGet, "/agent/connect", nil)
	named.Header.Set("X-Forwarded-Proto", "https")
	named.RemoteAddr = "10.0.0.7:1234"
	if got, _ := proxies.transportOf(named); got != TransportTLS {
		t.Fatalf("a named proxy address: got %q, want %q", got, TransportTLS)
	}
	neighbour := httptest.NewRequest(http.MethodGet, "/agent/connect", nil)
	neighbour.Header.Set("X-Forwarded-Proto", "https")
	neighbour.RemoteAddr = "10.0.0.8:1234"
	if got, _ := proxies.transportOf(neighbour); got != TransportPlain {
		t.Fatalf("an address next to the proxy: got %q, want %q", got, TransportPlain)
	}
}

func TestProxySpecIgnoresRubbishWithoutTrustingIt(t *testing.T) {
	p := parseProxies("not-an-address, 10.0.0.0/8")
	if !p.configured() {
		t.Fatal("a usable entry should still configure the set")
	}
	if !p.trusts("10.1.2.3:99") {
		t.Fatal("the usable entry should be honoured")
	}
	if p.trusts("192.168.1.1:99") {
		t.Fatal("the unusable entry must not widen the set")
	}
}
