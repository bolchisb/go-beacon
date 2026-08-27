package main

import (
	"fmt"
	"sync"
	"time"
)

// Which public key owns which agent id.
//
// Without this record enrolment would hand out an assertion for any id to
// anyone holding operator credentials, and the registry replaces a connection
// when a second one arrives under the same id: it closes the old session and
// serves the new one. Together that means a name could be taken over. Every
// terminal, forward and MCP call aimed at that id would land on the newcomer
// while the real machine sat disconnected, and nothing anywhere recorded that
// the identity had changed hands.
//
// So an id belongs to the first key that claims it. Re-enrolling the same
// machine sends the same key -- it keeps the private half and only refreshes
// the assertion -- and passes unremarked. A different key is refused until an
// operator says, in as many words, that this is a rebind.
//
// Vault is the only home. Enrolment already cannot proceed without Vault, since
// Vault is what signs the assertion, so there is nothing to lose by depending
// on it here and a cache would only be another copy to disagree with.
const agentKeysVaultPath = "beacon/agent-keys"

// kvStore is the slice of Vault this needs. An interface because the tests need
// somewhere to keep a map, not because a second implementation is planned.
type kvStore interface {
	kvGet(path string, out any) (bool, error)
	kvPut(path string, in any) error
}

type agentKeys struct {
	mu    sync.Mutex
	store kvStore
}

func newAgentKeys(v *vault) *agentKeys { return &agentKeys{store: v} }

// agentKeyRecord is what is remembered about one id.
type agentKeyRecord struct {
	PublicKey string    `json:"public_key"`
	BoundAt   time.Time `json:"bound_at"`
	BoundBy   string    `json:"bound_by,omitempty"`
	Rebinds   int       `json:"rebinds,omitempty"`
}

// errRebindRequired says the id is spoken for by another key. It is a refusal
// with an answer in it, because the legitimate case -- a machine rebuilt from
// scratch, holding a new key under its old name -- is common enough that a bare
// "denied" would be read as a bug.
type errRebindRequired struct {
	AgentID string
	BoundAt time.Time
}

func (e *errRebindRequired) Error() string {
	return fmt.Sprintf("%q is already enrolled with a different key, bound on %s. "+
		"If this machine was rebuilt, re-enrol it with `beacon enroll --rebind`; "+
		"if it was not, another machine is claiming this name.",
		e.AgentID, e.BoundAt.Format(time.RFC3339))
}

// claim binds an id to a key. Returns nil when the id is free, when the key is
// the one already bound to it, or when a rebind was asked for explicitly.
func (k *agentKeys) claim(id, publicKey, by string, rebind bool) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	all, err := k.loadLocked()
	if err != nil {
		return err
	}

	rec, known := all[id]
	switch {
	case !known:
		// First claim wins. On a relay upgraded from a build without this
		// record every id is unclaimed, so the first enrolment after the
		// upgrade is what binds it -- this does not reach back and protect
		// names that were already in use.
		all[id] = agentKeyRecord{PublicKey: publicKey, BoundAt: time.Now().UTC(), BoundBy: by}
	case rec.PublicKey == publicKey:
		return nil // a renewal, not a change
	case !rebind:
		return &errRebindRequired{AgentID: id, BoundAt: rec.BoundAt}
	default:
		all[id] = agentKeyRecord{
			PublicKey: publicKey,
			BoundAt:   time.Now().UTC(),
			BoundBy:   by,
			Rebinds:   rec.Rebinds + 1,
		}
	}

	return k.store.kvPut(agentKeysVaultPath, all)
}

func (k *agentKeys) loadLocked() (map[string]agentKeyRecord, error) {
	all := map[string]agentKeyRecord{}
	found, err := k.store.kvGet(agentKeysVaultPath, &all)
	if err != nil {
		return nil, err
	}
	if !found || all == nil {
		return map[string]agentKeyRecord{}, nil
	}
	return all, nil
}
