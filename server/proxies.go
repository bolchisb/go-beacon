package main

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Which peers the relay believes about TLS.
//
// A relay behind a terminating proxy is genuinely encrypted end to end even
// though the last hop -- proxy to relay, across an operator's own network -- is
// plain. The only evidence of that reaching the relay is X-Forwarded-Proto, and
// a header is written by whoever sent the request.
//
// That is fine right up until both doors are open at once, which is the normal
// shape: agents arrive through the proxy on 443, and the relay's own port is
// also reachable on the network for testing. An agent taking the direct route
// can set the header itself and be indistinguishable from one that came through
// the proxy -- and it is exactly the direct ones an operator wants to pick out.
//
// So the header is believed based on who is speaking, not on what it says. The
// immediate peer is the proxy or it is not; nothing an agent writes changes
// which of those is true.
type proxySet struct{ nets []netip.Prefix }

// parseProxies reads BEACON_TRUSTED_PROXIES: addresses or CIDR blocks, comma
// separated. Behind Docker or Kubernetes the proxy's address is assigned rather
// than chosen, so the useful value is usually the network it sits on.
func parseProxies(spec string) *proxySet {
	set := &proxySet{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			set.nets = append(set.nets, p)
			continue
		}
		if a, err := netip.ParseAddr(part); err == nil {
			set.nets = append(set.nets, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		slog.Error("ignoring an unusable entry in BEACON_TRUSTED_PROXIES", "value", part)
	}
	return set
}

// configured reports whether an operator has named any proxy at all.
func (p *proxySet) configured() bool { return p != nil && len(p.nets) > 0 }

// trusts reports whether a forwarded header from this peer should be believed.
func (p *proxySet) trusts(remoteAddr string) bool {
	if !p.configured() {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, n := range p.nets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

// transportOf reports how a connection reached the relay, and how sure the
// relay is about it. The second value is a sentence for whoever is reading the
// dashboard, because "TLS 1.3" and "a proxy said so" are both answers to the
// same question and want different words.
//
// Reading it here, off the request, is not a convenience: for an agent the
// connection becomes a raw tunnel moments later, and by then there is no
// *tls.Conn left to ask and no HTTP request left to carry the header.
func (p *proxySet) transportOf(r *http.Request) (Transport, string) {
	if r.TLS != nil {
		return TransportTLS, "this relay terminated the handshake, " + tls.VersionName(r.TLS.Version)
	}
	if !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return TransportPlain, ""
	}
	if p.trusts(r.RemoteAddr) {
		// The peer is a proxy this operator named, so the claim is as good as
		// the relay is going to get, and better than what it could check
		// itself: the encrypted hop is the one it never sees.
		return TransportTLS, "terminated by a trusted proxy in front of this relay"
	}
	if p.configured() {
		// Somebody who is not the proxy said https. Worth a line in the log:
		// on a relay whose own port is reachable, this is what an agent
		// dressing up a plain connection looks like.
		slog.Debug("ignoring a forwarded protocol from an untrusted peer", "remote", r.RemoteAddr)
		return TransportPlain, ""
	}
	// No proxy configured, so the relay has nothing to check the claim against
	// and does not pretend otherwise.
	return TransportProxyTLS, ""
}
