package protocol

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Agent identity.
//
// An agent generates a keypair on the machine it runs on and never sends the
// private half anywhere. Enrolment is authorised by an operator's own username
// and password -- typed once, by a human, at install -- and produces an
// Assertion: a statement signed by the relay's Vault that binds an agent id to
// that public key until a stated expiry.
//
// On every connection the agent signs a challenge the relay just issued. The
// assertion proves which key belongs to the id; the signature proves the agent
// still holds it. Neither is replayable, and the relay verifies both without
// asking Vault anything, so a sealed Vault stops enrolment without disconnecting
// a fleet.

const (
	// ChallengePath is where an agent asks for something fresh to sign. It has
	// to be reachable before the agent has proved anything, which is why it is
	// separate from the tunnel itself.
	ChallengePath = "/agent/challenge"

	// EnrollPath issues an assertion. Authorised by operator credentials, not
	// by anything the agent holds -- the agent has nothing yet.
	EnrollPath = "/api/agents/enroll"

	HeaderAssertion = "X-Beacon-Assertion"
	HeaderChallenge = "X-Beacon-Challenge"
	HeaderSignature = "X-Beacon-Signature"
)

// Assertion is what Vault signs. It is carried as compact JSON so that what is
// verified is exactly the bytes that were signed, with no re-encoding in
// between -- re-marshalling to compare would make key order part of the
// security argument.
type Assertion struct {
	AgentID   string    `json:"agent_id"`
	PublicKey string    `json:"public_key"` // base64 raw ed25519
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SignedAssertion travels as "<base64 of the JSON>.<vault signature>".
type SignedAssertion struct {
	Document  []byte // the exact bytes that were signed
	Signature string // vault:vN:base64
}

func (s SignedAssertion) String() string {
	return base64.RawURLEncoding.EncodeToString(s.Document) + "." + s.Signature
}

func ParseSignedAssertion(v string) (SignedAssertion, Assertion, error) {
	doc, sig, ok := strings.Cut(v, ".")
	if !ok {
		return SignedAssertion{}, Assertion{}, fmt.Errorf("malformed assertion")
	}
	raw, err := base64.RawURLEncoding.DecodeString(doc)
	if err != nil {
		return SignedAssertion{}, Assertion{}, fmt.Errorf("malformed assertion body: %w", err)
	}
	var a Assertion
	if err := json.Unmarshal(raw, &a); err != nil {
		return SignedAssertion{}, Assertion{}, fmt.Errorf("malformed assertion body: %w", err)
	}
	if a.AgentID == "" || a.PublicKey == "" {
		return SignedAssertion{}, Assertion{}, fmt.Errorf("assertion is missing an id or a key")
	}
	return SignedAssertion{Document: raw, Signature: sig}, a, nil
}

// PublicKeyBytes decodes the key the assertion binds to the id.
func (a Assertion) PublicKeyBytes() (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(a.PublicKey)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

func (a Assertion) Expired(now time.Time) bool {
	return !a.ExpiresAt.IsZero() && now.After(a.ExpiresAt)
}

// MarshalAssertion produces the bytes to sign. Used by the relay when issuing;
// the agent never re-encodes, it keeps what it was given.
func MarshalAssertion(a Assertion) ([]byte, error) { return json.Marshal(a) }
