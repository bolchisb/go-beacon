package main

import (
	"encoding/json"
	"errors"
	"testing"
)

// fakeKV is a map with the two methods agentKeys uses. Round-tripping through
// JSON is not laziness: it is what Vault does, and it is what would expose a
// record that does not survive the trip.
type fakeKV struct {
	data map[string][]byte
	puts int
}

func newFakeKV() *fakeKV { return &fakeKV{data: map[string][]byte{}} }

func (f *fakeKV) kvGet(path string, out any) (bool, error) {
	raw, ok := f.data[path]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (f *fakeKV) kvPut(path string, in any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	f.data[path] = raw
	f.puts++
	return nil
}

func TestAnAgentIdBelongsToItsFirstKey(t *testing.T) {
	kv := newFakeKV()
	k := &agentKeys{store: kv}

	if err := k.claim("target-01", "key-one", "operator", false); err != nil {
		t.Fatalf("an unclaimed id should bind: %v", err)
	}

	// Re-enrolling the same machine sends the same key: it keeps the private
	// half and only wants a fresh assertion. That must not look like a change.
	before := kv.puts
	if err := k.claim("target-01", "key-one", "operator", false); err != nil {
		t.Fatalf("a renewal with the same key should pass: %v", err)
	}
	if kv.puts != before {
		t.Fatal("a renewal should not rewrite the record")
	}

	// A different key is the takeover this exists to stop.
	err := k.claim("target-01", "key-two", "operator", false)
	var rebind *errRebindRequired
	if !errors.As(err, &rebind) {
		t.Fatalf("a different key must be refused, got %v", err)
	}
	if rebind.AgentID != "target-01" {
		t.Fatalf("the refusal should name the id, got %q", rebind.AgentID)
	}

	// Unless somebody says so, which is the rebuilt-machine case.
	if err := k.claim("target-01", "key-two", "operator", true); err != nil {
		t.Fatalf("an explicit rebind should be allowed: %v", err)
	}

	// And the rebind is a move, not an addition: the old key is out.
	if err := k.claim("target-01", "key-one", "operator", false); !errors.As(err, &rebind) {
		t.Fatalf("the previous key should no longer own the id, got %v", err)
	}
}

func TestAgentIdsDoNotCollideWithEachOther(t *testing.T) {
	k := &agentKeys{store: newFakeKV()}

	if err := k.claim("target-01", "key-one", "", false); err != nil {
		t.Fatal(err)
	}
	if err := k.claim("target-02", "key-two", "", false); err != nil {
		t.Fatalf("a second id is unaffected by the first: %v", err)
	}
	if err := k.claim("target-01", "key-one", "", false); err != nil {
		t.Fatalf("the first binding survived the second: %v", err)
	}
}

func TestRebindsAreCounted(t *testing.T) {
	kv := newFakeKV()
	k := &agentKeys{store: kv}

	if err := k.claim("target-01", "key-one", "alice", false); err != nil {
		t.Fatal(err)
	}
	if err := k.claim("target-01", "key-two", "bob", true); err != nil {
		t.Fatal(err)
	}

	all := map[string]agentKeyRecord{}
	if _, err := kv.kvGet(agentKeysVaultPath, &all); err != nil {
		t.Fatal(err)
	}
	rec := all["target-01"]
	if rec.Rebinds != 1 {
		t.Fatalf("rebinds: got %d, want 1", rec.Rebinds)
	}
	if rec.BoundBy != "bob" {
		t.Fatalf("bound by: got %q, want %q", rec.BoundBy, "bob")
	}
}
