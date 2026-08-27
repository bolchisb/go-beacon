package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// This machine's identity to the relay.
//
// The keypair is generated here and the private half never leaves. Enrolment
// trades an operator's username and password -- typed once, by whoever is
// installing -- for an assertion the relay's Vault signed. What ends up on the
// machine is its own identity and nothing that can command any other machine.

// ensureKeypair generates one if this machine has none yet, and returns the
// private key either way.
func ensureKeypair(cfg *Config) (ed25519.PrivateKey, error) {
	if cfg.AgentKey != "" {
		raw, err := base64.StdEncoding.DecodeString(cfg.AgentKey)
		if err != nil {
			return nil, fmt.Errorf("the stored agent key is unreadable: %w", err)
		}
		if len(raw) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("the stored agent key is the wrong size")
		}
		return ed25519.PrivateKey(raw), nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	cfg.AgentKey = base64.StdEncoding.EncodeToString(priv)
	return priv, nil
}

func publicKeyOf(priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// enroll asks the relay for an assertion. The operator credentials are used for
// this one call and never stored.
func enroll(server, caFile, username, password, agentID, publicKey string, rebind bool) (string, error) {
	u, err := relayURL(server, protocol.EnrollPath)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{
		"username": username, "password": password,
		"agent_id": agentID, "public_key": publicKey,
		"rebind": rebind,
	})
	if err != nil {
		return "", err
	}
	client, err := relayClient(caFile)
	if err != nil {
		return "", err
	}
	resp, err := client.Post(u, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		Assertion string `json:"assertion"`
		ExpiresAt string `json:"expires_at"`
		Error     string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return "", fmt.Errorf("%s", out.Error)
		}
		return "", fmt.Errorf("the relay refused enrolment: %s", resp.Status)
	}
	if out.Assertion == "" {
		return "", fmt.Errorf("the relay accepted enrolment but issued no assertion")
	}
	return out.Assertion, nil
}

// fetchChallenge asks for something fresh to sign. Doing this on every connect
// is what stops a captured handshake being replayed.
func fetchChallenge(server, caFile string) (string, error) {
	u, err := relayURL(server, protocol.ChallengePath)
	if err != nil {
		return "", err
	}
	client, err := relayClient(caFile)
	if err != nil {
		return "", err
	}
	resp, err := client.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the relay would not issue a challenge: %s", resp.Status)
	}
	var out struct {
		Challenge string `json:"challenge"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Challenge == "" {
		return "", fmt.Errorf("the relay issued an empty challenge")
	}
	return out.Challenge, nil
}

// identityHeaders proves who this machine is: the assertion says which key
// belongs to this id, the signature says the machine still holds that key.
func identityHeaders(cfg *resolved, priv ed25519.PrivateKey) (http.Header, error) {
	if cfg.Assertion == "" {
		return nil, fmt.Errorf("this agent is not enrolled: run `beacon enroll` on this machine")
	}
	nonce, err := fetchChallenge(cfg.Server, cfg.CAFile)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set(protocol.HeaderAssertion, cfg.Assertion)
	h.Set(protocol.HeaderChallenge, nonce)
	h.Set(protocol.HeaderSignature,
		base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(nonce))))
	return h, nil
}

func relayURL(server, path string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("server URL %q has no host", server)
	}
	u.Path = path
	u.RawQuery = ""
	return u.String(), nil
}

func relayClient(caFile string) (*http.Client, error) {
	tlsCfg, err := tlsConfig(caFile)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}, nil
}

var _ = tls.Config{}
