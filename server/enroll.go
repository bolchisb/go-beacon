package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// Agent enrolment and the check on every connection.
//
// Enrolment is authorised by an operator's own username and password, typed by
// a human at install time. That credential is used and discarded: what the
// agent keeps is its own keypair and an assertion Vault signed for it, so
// compromising a target machine yields that machine's identity and nothing
// else. In particular it does not yield anything that can command the fleet.

const (
	challengeTTL     = 2 * time.Minute
	assertionTTL     = 90 * 24 * time.Hour
	maxChallenges    = 4096
	challengeEntropy = 32
)

// challenges holds the nonces handed out but not yet used. They are single use
// and short lived, which is what stops a captured handshake being replayed.
// In memory on purpose: a challenge outliving a restart would have no value,
// and the relay keeps no state it does not have to.
type challenges struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newChallenges() *challenges { return &challenges{seen: map[string]time.Time{}} }

func (c *challenges) issue() (string, error) {
	b := make([]byte, challengeEntropy)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	n := base64.RawURLEncoding.EncodeToString(b)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked()
	// A flood of unredeemed challenges must not grow without bound. Dropping
	// the oldest is safe: the agent that asked will simply ask again.
	if len(c.seen) >= maxChallenges {
		var oldest string
		var at time.Time
		for k, v := range c.seen {
			if oldest == "" || v.Before(at) {
				oldest, at = k, v
			}
		}
		delete(c.seen, oldest)
	}
	c.seen[n] = time.Now()
	return n, nil
}

// redeem consumes a challenge. A second attempt with the same one fails, which
// is the whole point.
func (c *challenges) redeem(n string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked()
	if _, ok := c.seen[n]; !ok {
		return false
	}
	delete(c.seen, n)
	return true
}

func (c *challenges) expireLocked() {
	cutoff := time.Now().Add(-challengeTTL)
	for k, v := range c.seen {
		if v.Before(cutoff) {
			delete(c.seen, k)
		}
	}
}

func (s *Server) handleAgentChallenge(w http.ResponseWriter, r *http.Request) {
	n, err := s.challenges.issue()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not issue a challenge"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"challenge": n})
}

type enrollRequest struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	AgentID   string `json:"agent_id"`
	PublicKey string `json:"public_key"`
}

// handleEnroll issues an assertion binding an agent id to the public key the
// agent generated for itself.
//
// It is authorised by operator credentials rather than by anything the agent
// holds, because at this point the agent holds nothing. The password is checked
// and dropped; it is never stored, forwarded, or written to the machine being
// enrolled.
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req enrollRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Either an operator's own credentials, or the admin token for a relay that
	// has no account yet.
	authorised := s.ops.verify(req.Username, req.Password) ||
		(req.Username == "" && s.auth.matches(strings.TrimSpace(req.Password)))
	if !authorised {
		time.Sleep(500 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "those operator credentials were not accepted"})
		return
	}

	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" || len(agentID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a usable agent id is required"})
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the public key is not a valid ed25519 key"})
		return
	}

	now := time.Now().UTC()
	a := protocol.Assertion{
		AgentID:   agentID,
		PublicKey: req.PublicKey,
		IssuedAt:  now,
		ExpiresAt: now.Add(assertionTTL),
	}
	doc, err := protocol.MarshalAssertion(a)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sig, err := s.vault.Sign(doc)
	if err != nil {
		// Vault being unreachable stops enrolment and nothing else. Saying so
		// plainly beats a generic failure, because the fix is elsewhere.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Vault could not sign this: " + err.Error()})
		return
	}

	signed := protocol.SignedAssertion{Document: doc, Signature: sig}
	slog.Info("agent enrolled", "agent", agentID, "by", req.Username, "expires", a.ExpiresAt)
	s.events.Publish(Event{Type: "enroll", AgentID: agentID,
		Message: "enrolled by " + operatorLabel(req.Username)})

	writeJSON(w, http.StatusOK, map[string]string{
		"assertion":  signed.String(),
		"expires_at": a.ExpiresAt.Format(time.RFC3339),
	})
}

func operatorLabel(u string) string {
	if u == "" {
		return "the admin token"
	}
	return u
}

// verifyAgent checks the two things a connecting agent has to show: an
// assertion Vault signed, and a signature over a challenge this relay issued
// moments ago. Returns the id the assertion carries, which from here on is the
// agent's identity -- the header it sends becomes descriptive only.
func (s *Server) verifyAgent(r *http.Request) (string, error) {
	raw := r.Header.Get(protocol.HeaderAssertion)
	if raw == "" {
		return "", fmt.Errorf("this agent has not been enrolled: run `beacon enroll` on it")
	}
	signed, a, err := protocol.ParseSignedAssertion(raw)
	if err != nil {
		return "", err
	}
	if !s.vault.Verify(signed.Document, signed.Signature) {
		return "", fmt.Errorf("the assertion is not signed by this relay's Vault")
	}
	if a.Expired(time.Now()) {
		return "", fmt.Errorf("the assertion expired on %s: run `beacon enroll` on it again",
			a.ExpiresAt.Format(time.RFC3339))
	}

	nonce := r.Header.Get(protocol.HeaderChallenge)
	if !s.challenges.redeem(nonce) {
		return "", fmt.Errorf("the challenge is unknown, already used, or expired")
	}

	sig, err := base64.StdEncoding.DecodeString(r.Header.Get(protocol.HeaderSignature))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", fmt.Errorf("the challenge signature is malformed")
	}
	pub, err := a.PublicKeyBytes()
	if err != nil {
		return "", err
	}
	if !ed25519.Verify(pub, []byte(nonce), sig) {
		return "", fmt.Errorf("the challenge signature does not match the enrolled key")
	}
	return a.AgentID, nil
}
