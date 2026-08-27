package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// enrolled builds a relay that has signed one assertion, plus the agent's
// private key, without needing a Vault: the transit keypair is generated here
// and handed to both sides.
func enrolled(t *testing.T, agentID string, expires time.Time) (*Server, ed25519.PrivateKey, string) {
	t.Helper()
	vpub, vpriv, _ := ed25519.GenerateKey(nil)
	apub, apriv, _ := ed25519.GenerateKey(nil)

	v := &vault{pubKeys: map[int]ed25519.PublicKey{1: vpub}}
	s := &Server{
		vault:      v,
		challenges: newChallenges(),
		events:     newEventBus(),
	}

	a := protocol.Assertion{
		AgentID:   agentID,
		PublicKey: base64.StdEncoding.EncodeToString(apub),
		IssuedAt:  time.Now().Add(-time.Hour),
		ExpiresAt: expires,
	}
	doc, err := protocol.MarshalAssertion(a)
	if err != nil {
		t.Fatal(err)
	}
	signed := protocol.SignedAssertion{
		Document:  doc,
		Signature: "vault:v1:" + base64.StdEncoding.EncodeToString(ed25519.Sign(vpriv, doc)),
	}
	return s, apriv, signed.String()
}

// connectAs builds the request an agent makes, redeeming a real challenge.
func connectAs(t *testing.T, s *Server, assertion string, priv ed25519.PrivateKey) *http.Request {
	t.Helper()
	nonce, err := s.challenges.issue()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, protocol.ConnectPath, nil)
	r.Header.Set(protocol.HeaderAssertion, assertion)
	r.Header.Set(protocol.HeaderChallenge, nonce)
	r.Header.Set(protocol.HeaderSignature,
		base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(nonce))))
	return r
}

func TestAnEnrolledAgentIsAccepted(t *testing.T) {
	s, priv, assertion := enrolled(t, "target-01", time.Now().Add(time.Hour))
	id, err := s.verifyAgent(connectAs(t, s, assertion, priv))
	if err != nil {
		t.Fatalf("a valid agent was refused: %v", err)
	}
	if id != "target-01" {
		t.Errorf("id is %q, want the one in the assertion", id)
	}
}

func TestIdentityComesFromTheAssertionNotTheHeader(t *testing.T) {
	// The hello header is attacker-controlled. If it could name the agent, any
	// enrolled machine could receive another machine's commands.
	s, priv, assertion := enrolled(t, "target-01", time.Now().Add(time.Hour))
	r := connectAs(t, s, assertion, priv)
	r.Header.Set(protocol.HeaderAgentID, "production-db")

	id, err := s.verifyAgent(r)
	if err != nil {
		t.Fatal(err)
	}
	if id != "target-01" {
		t.Errorf("id is %q, want the assertion to win over the header", id)
	}
}

func TestAnUnenrolledAgentIsRefused(t *testing.T) {
	s, _, _ := enrolled(t, "target-01", time.Now().Add(time.Hour))
	r := httptest.NewRequest(http.MethodGet, protocol.ConnectPath, nil)
	if _, err := s.verifyAgent(r); err == nil {
		t.Fatal("an agent with no assertion was accepted")
	}
}

func TestATamperedAssertionIsRefused(t *testing.T) {
	s, priv, assertion := enrolled(t, "target-01", time.Now().Add(time.Hour))

	// Re-point the assertion at another machine, keeping the signature.
	_, a, err := protocol.ParseSignedAssertion(assertion)
	if err != nil {
		t.Fatal(err)
	}
	a.AgentID = "production-db"
	doc, _ := protocol.MarshalAssertion(a)
	_, sig, _ := splitAssertion(assertion)
	forged := base64.RawURLEncoding.EncodeToString(doc) + "." + sig

	if _, err := s.verifyAgent(connectAs(t, s, forged, priv)); err == nil {
		t.Fatal("a tampered assertion was accepted")
	}
}

func TestAnotherKeyCannotUseTheAssertion(t *testing.T) {
	// Holding a copy of someone's assertion is not enough; the private key
	// never left the machine it was generated on.
	s, _, assertion := enrolled(t, "target-01", time.Now().Add(time.Hour))
	_, impostor, _ := ed25519.GenerateKey(nil)

	if _, err := s.verifyAgent(connectAs(t, s, assertion, impostor)); err == nil {
		t.Fatal("a signature from an unrelated key was accepted")
	}
}

func TestAChallengeCannotBeReplayed(t *testing.T) {
	s, priv, assertion := enrolled(t, "target-01", time.Now().Add(time.Hour))
	r := connectAs(t, s, assertion, priv)

	if _, err := s.verifyAgent(r); err != nil {
		t.Fatalf("the first use failed: %v", err)
	}
	// The same request again -- a captured handshake.
	if _, err := s.verifyAgent(r); err == nil {
		t.Fatal("a replayed handshake was accepted")
	}
}

func TestAnExpiredAssertionIsRefused(t *testing.T) {
	s, priv, assertion := enrolled(t, "target-01", time.Now().Add(-time.Minute))
	_, err := s.verifyAgent(connectAs(t, s, assertion, priv))
	if err == nil {
		t.Fatal("an expired assertion was accepted")
	}
}

func TestChallengesExpireAndAreSingleUse(t *testing.T) {
	c := newChallenges()
	n, err := c.issue()
	if err != nil {
		t.Fatal(err)
	}
	if !c.redeem(n) {
		t.Fatal("a fresh challenge could not be redeemed")
	}
	if c.redeem(n) {
		t.Error("a challenge was redeemed twice")
	}
	if c.redeem("never-issued") {
		t.Error("an unissued challenge was redeemed")
	}

	// One that has aged past its window is gone even if never used.
	old, _ := c.issue()
	c.mu.Lock()
	c.seen[old] = time.Now().Add(-challengeTTL - time.Second)
	c.mu.Unlock()
	if c.redeem(old) {
		t.Error("an expired challenge was redeemed")
	}
}

// splitAssertion is a test helper; the production path never needs the halves
// separately because it verifies the document it was given.
func splitAssertion(v string) (doc, sig string, ok bool) {
	for i := 0; i < len(v); i++ {
		if v[i] == '.' {
			return v[:i], v[i+1:], true
		}
	}
	return "", "", false
}
