package store

import (
	"encoding/json"
	"testing"
	"time"
)

func entry(actor, action, entity string, ts time.Time) Entry {
	after := json.RawMessage(`{"enabled":false}`)
	return Entry{
		ActorSub:   actor,
		ActorRoles: []string{"platform-admin"},
		Action:     action,
		Entity:     entity,
		EntityID:   "42",
		After:      &after,
		IP:         "10.0.0.1",
		UA:         "test-agent",
		TS:         ts,
	}
}

// simulateChain appends entries the way PGStore.Append does and returns them.
func simulateChain(es []Entry) []Entry {
	prev := ""
	for i := range es {
		es[i].ID = int64(i + 1)
		es[i].PrevHash = prev
		es[i].Hash = ChainHash(prev, es[i])
		prev = es[i].Hash
	}
	return es
}

// verifyChain mirrors PGStore.Verify logic over an in-memory slice.
func verifyChain(es []Entry) (badID int64) {
	prev := ""
	for _, e := range es {
		if e.PrevHash != prev {
			return e.ID
		}
		if ChainHash(prev, e) != e.Hash {
			return e.ID
		}
		prev = e.Hash
	}
	return 0
}

func TestChainHashDeterministicAndUnique(t *testing.T) {
	ts := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	e1 := entry("alice", "user.create", "user", ts)
	e2 := entry("alice", "user.create", "user", ts.Add(time.Second))
	e3 := entry("bob", "user.create", "user", ts)

	if ChainHash("", e1) != ChainHash("", e1) {
		t.Fatal("hash not deterministic")
	}
	if ChainHash("", e1) == ChainHash("", e2) {
		t.Fatal("ts must feed the hash")
	}
	if ChainHash("", e1) == ChainHash("", e3) {
		t.Fatal("actor must feed the hash")
	}
	if ChainHash("", e1) == ChainHash("abc", e1) {
		t.Fatal("prev_hash must feed the hash")
	}
	if got := len(ChainHash("", e1)); got != 64 {
		t.Fatalf("sha256 hex length = %d, want 64", got)
	}
}

func TestChainVerifiesAndDetectsTampering(t *testing.T) {
	ts := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	chain := simulateChain([]Entry{
		entry("alice", "user.create", "user", ts),
		entry("alice", "toggle.update", "feature_toggle", ts.Add(time.Second)),
		entry("carol", "payment.create", "fare_payment", ts.Add(2*time.Second)),
	})
	if bad := verifyChain(chain); bad != 0 {
		t.Fatalf("clean chain flagged at id %d", bad)
	}

	// Tamper: edit a payload field without recomputing hashes.
	tampered := append([]Entry(nil), chain...)
	tampered[1].ActorSub = "mallory"
	if bad := verifyChain(tampered); bad != 2 {
		t.Fatalf("payload tamper: bad id = %d, want 2", bad)
	}

	// Tamper: delete the middle row (prev linkage breaks).
	deleted := []Entry{chain[0], chain[2]}
	if bad := verifyChain(deleted); bad != 3 {
		t.Fatalf("row deletion: bad id = %d, want 3", bad)
	}

	// Tamper: recompute row 1's hash to hide the edit — row 2's stored
	// prev_hash then no longer matches.
	rehash := append([]Entry(nil), chain...)
	rehash[0].ActorSub = "mallory"
	rehash[0].Hash = ChainHash("", rehash[0])
	if bad := verifyChain(rehash); bad != 2 {
		t.Fatalf("rehash attack: bad id = %d, want 2", bad)
	}
}

func TestRawTextNilSafe(t *testing.T) {
	if got := rawText(nil); got != "" {
		t.Fatalf("rawText(nil) = %q, want empty", got)
	}
}
