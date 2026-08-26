package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"strconv"
	"testing"
)

// signAs formats a signature the way transit does, so these tests exercise the
// exact string the relay will receive.
func signAs(version int, priv ed25519.PrivateKey, msg []byte) string {
	sig := ed25519.Sign(priv, msg)
	return "vault:v" + strconv.Itoa(version) + ":" + base64.StdEncoding.EncodeToString(sig)
}

func TestVerifyAcceptsAGenuineSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := &vault{pubKeys: map[int]ed25519.PublicKey{1: pub}}

	msg := []byte(`{"agent":"build-vm-01"}`)
	if !v.Verify(msg, signAs(1, priv, msg)) {
		t.Fatal("a genuine signature was rejected")
	}
}

func TestVerifyRejectsTamperingAndForgery(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, other, _ := ed25519.GenerateKey(nil)
	v := &vault{pubKeys: map[int]ed25519.PublicKey{1: pub}}

	msg := []byte(`{"agent":"build-vm-01"}`)
	good := signAs(1, priv, msg)

	if v.Verify([]byte(`{"agent":"attacker"}`), good) {
		t.Error("a signature was accepted over a different message")
	}
	if v.Verify(msg, signAs(1, other, msg)) {
		t.Error("a signature from an unrelated key was accepted")
	}
	// Version 2 is not a key this relay knows, so it cannot be trusted even
	// though the signature itself is well formed.
	if v.Verify(msg, signAs(2, priv, msg)) {
		t.Error("a signature from an unknown key version was accepted")
	}
}

func TestVerifySurvivesKeyRotation(t *testing.T) {
	// The point of caching every version: assertions signed before a rotation
	// stay valid until they expire on their own.
	oldPub, oldPriv, _ := ed25519.GenerateKey(nil)
	newPub, newPriv, _ := ed25519.GenerateKey(nil)
	v := &vault{pubKeys: map[int]ed25519.PublicKey{1: oldPub, 2: newPub}}

	msg := []byte("assertion")
	if !v.Verify(msg, signAs(1, oldPriv, msg)) {
		t.Error("an assertion signed before rotation stopped verifying")
	}
	if !v.Verify(msg, signAs(2, newPriv, msg)) {
		t.Error("an assertion signed after rotation did not verify")
	}
}

func TestParseSignatureRejectsMalformedInput(t *testing.T) {
	for _, s := range []string{
		"", "vault:v1", "notvault:v1:AAAA", "vault:x1:AAAA", "vault:v0:AAAA",
		"vault:v1:not-base64!!", "vault:v1:" + base64.StdEncoding.EncodeToString([]byte("too short")),
	} {
		if _, _, ok := parseSignature(s); ok {
			t.Errorf("accepted malformed signature %q", s)
		}
	}
}

func TestVerifyWithNoKeysAlwaysFails(t *testing.T) {
	// A relay that has never reached Vault and has no cache must reject
	// everything rather than fall open.
	_, priv, _ := ed25519.GenerateKey(nil)
	v := &vault{pubKeys: map[int]ed25519.PublicKey{}}
	msg := []byte("assertion")
	if v.Verify(msg, signAs(1, priv, msg)) {
		t.Fatal("a relay with no public keys accepted a signature")
	}
}

func TestCachedKeysSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(nil)

	writer := &vault{stateDir: dir, pubKeys: map[int]ed25519.PublicKey{}}
	if err := writer.saveCachedKeys(map[string]string{
		"1": base64.StdEncoding.EncodeToString(pub),
	}); err != nil {
		t.Fatalf("saving the cache failed: %v", err)
	}

	// A fresh relay that cannot reach Vault at all.
	reader := &vault{stateDir: dir, pubKeys: map[int]ed25519.PublicKey{}}
	if err := reader.loadCachedKeys(); err != nil {
		t.Fatalf("loading the cache failed: %v", err)
	}
	msg := []byte("assertion")
	if !reader.Verify(msg, signAs(1, priv, msg)) {
		t.Fatal("an agent could not be verified from the cache with Vault down")
	}

	info, err := os.Stat(reader.cachePath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache mode is %o, want 600", perm)
	}
}

func TestLoadCachedKeysFailsCleanlyOnGarbage(t *testing.T) {
	dir := t.TempDir()
	v := &vault{stateDir: dir, pubKeys: map[int]ed25519.PublicKey{}}
	if err := os.WriteFile(v.cachePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := v.loadCachedKeys(); err == nil {
		t.Fatal("garbage in the cache was accepted")
	}
}
