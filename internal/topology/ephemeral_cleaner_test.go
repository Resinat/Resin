package topology

import (
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/subscription"
)

// TestEphemeralCleaner_TOCTOU_RecoveryBetweenScans verifies that a node
// recovering in the window between the first scan (evictSet) and the
// second check (confirmedEvict) is NOT evicted.
//
// Timeline:
//  1. Node is circuit-broken with stale CircuitOpenSince → enters evictSet.
//  2. betweenScans hook fires: clears CircuitOpenSince (simulating recovery).
//  3. Second check re-reads CircuitOpenSince=0 → node is NOT confirmed.
//  4. Node remains in subscription's managed nodes.
func TestEphemeralCleaner_TOCTOU_RecoveryBetweenScans(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := NewGlobalNodePool(PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: 2,
	})

	sub := subscription.NewSubscription("sub-toctou", "ephemeral-sub", "http://example.com", true, true)
	subMgr.Register(sub)

	hash := node.HashFromRawOptions([]byte(`{"type":"toctou-node"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"toctou-node"}`), sub.ID)

	// Populate subscription's managed nodes.
	sub.ManagedNodes().Store(hash, []string{"tag1"})

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}

	// Node is circuit-broken long enough to qualify for eviction.
	pastTime := time.Now().Add(-1 * time.Hour).UnixNano()
	entry.CircuitOpenSince.Store(pastTime)

	cleaner := NewEphemeralCleaner(subMgr, pool, 30*time.Second)

	// The hook fires between first scan and second check, simulating
	// a recovery that happens in the TOCTOU window.
	hookCalled := false
	cleaner.sweepWithHook(func() {
		hookCalled = true
		// Simulate recovery: clear circuit.
		entry.CircuitOpenSince.Store(0)
		entry.FailureCount.Store(0)
	})

	if !hookCalled {
		t.Fatal("betweenScans hook was not called — node may not have been a candidate")
	}

	// The node should still be in the subscription's managed nodes.
	_, still := sub.ManagedNodes().Load(hash)
	if !still {
		t.Fatal("TOCTOU regression: recovered node was evicted from subscription")
	}

	// The node should still be in the pool.
	_, poolOK := pool.GetEntry(hash)
	if !poolOK {
		t.Fatal("TOCTOU regression: recovered node was removed from pool")
	}
}

// TestEphemeralCleaner_ConfirmedEviction verifies that a node that remains
// circuit-broken through both checks IS evicted correctly.
func TestEphemeralCleaner_ConfirmedEviction(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := NewGlobalNodePool(PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: 2,
	})

	sub := subscription.NewSubscription("sub-evict", "ephemeral-sub", "http://example.com", true, true)
	subMgr.Register(sub)

	hash := node.HashFromRawOptions([]byte(`{"type":"evict-node"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"evict-node"}`), sub.ID)
	sub.ManagedNodes().Store(hash, []string{"tag1"})

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}

	pastTime := time.Now().Add(-1 * time.Hour).UnixNano()
	entry.CircuitOpenSince.Store(pastTime)

	cleaner := NewEphemeralCleaner(subMgr, pool, 30*time.Second)
	cleaner.sweep()

	_, still := sub.ManagedNodes().Load(hash)
	if still {
		t.Fatal("expected circuit-broken node to be evicted from subscription")
	}
}

// TestEphemeralCleaner_NonEphemeralSkipped verifies non-ephemeral subs are skipped.
func TestEphemeralCleaner_NonEphemeralSkipped(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := NewGlobalNodePool(PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: 2,
	})

	sub := subscription.NewSubscription("sub-persist", "persistent-sub", "http://example.com", true, false) // NOT ephemeral
	subMgr.Register(sub)

	hash := node.HashFromRawOptions([]byte(`{"type":"persistent-node"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"persistent-node"}`), sub.ID)
	sub.ManagedNodes().Store(hash, []string{"tag1"})

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}

	pastTime := time.Now().Add(-1 * time.Hour).UnixNano()
	entry.CircuitOpenSince.Store(pastTime)

	cleaner := NewEphemeralCleaner(subMgr, pool, 30*time.Second)
	cleaner.sweep()

	_, still := sub.ManagedNodes().Load(hash)
	if !still {
		t.Fatal("non-ephemeral sub should not have nodes evicted")
	}
}
