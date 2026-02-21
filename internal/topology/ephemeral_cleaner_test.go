package topology

import (
	"runtime"
	"sync/atomic"
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
		MaxConsecutiveFailures: func() int { return 2 },
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

	cleaner := NewEphemeralCleaner(subMgr, pool, func() time.Duration { return 30 * time.Second })

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
		MaxConsecutiveFailures: func() int { return 2 },
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

	cleaner := NewEphemeralCleaner(subMgr, pool, func() time.Duration { return 30 * time.Second })
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
		MaxConsecutiveFailures: func() int { return 2 },
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

	cleaner := NewEphemeralCleaner(subMgr, pool, func() time.Duration { return 30 * time.Second })
	cleaner.sweep()

	_, still := sub.ManagedNodes().Load(hash)
	if !still {
		t.Fatal("non-ephemeral sub should not have nodes evicted")
	}
}

func TestEphemeralCleaner_DynamicEvictDelayPulled(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := NewGlobalNodePool(PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 2 },
	})

	sub := subscription.NewSubscription("sub-dynamic", "ephemeral-sub", "http://example.com", true, true)
	subMgr.Register(sub)

	hash := node.HashFromRawOptions([]byte(`{"type":"dynamic-node"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"dynamic-node"}`), sub.ID)
	sub.ManagedNodes().Store(hash, []string{"tag1"})

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	entry.CircuitOpenSince.Store(time.Now().Add(-2 * time.Minute).UnixNano())

	var evictDelayNs atomic.Int64
	evictDelayNs.Store(int64(10 * time.Minute))

	cleaner := NewEphemeralCleaner(
		subMgr,
		pool,
		func() time.Duration { return time.Duration(evictDelayNs.Load()) },
	)

	// Delay too long: should not evict.
	cleaner.sweep()
	if _, still := sub.ManagedNodes().Load(hash); !still {
		t.Fatal("node should not be evicted with long evict delay")
	}

	// Shrink delay dynamically: next sweep should evict.
	evictDelayNs.Store(int64(30 * time.Second))
	cleaner.sweep()
	if _, still := sub.ManagedNodes().Load(hash); still {
		t.Fatal("node should be evicted after evict delay shrinks")
	}
}

func TestEphemeralCleaner_SweepSubscriptionsInParallel(t *testing.T) {
	oldMaxProcs := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(oldMaxProcs)

	subMgr := NewSubscriptionManager()
	pool := NewGlobalNodePool(PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 2 },
	})

	sub1 := subscription.NewSubscription("sub-1", "ephemeral-1", "http://example.com/1", true, true)
	sub2 := subscription.NewSubscription("sub-2", "ephemeral-2", "http://example.com/2", true, true)
	subMgr.Register(sub1)
	subMgr.Register(sub2)

	hash1 := node.HashFromRawOptions([]byte(`{"type":"parallel-node-1"}`))
	hash2 := node.HashFromRawOptions([]byte(`{"type":"parallel-node-2"}`))

	pool.AddNodeFromSub(hash1, []byte(`{"type":"parallel-node-1"}`), sub1.ID)
	pool.AddNodeFromSub(hash2, []byte(`{"type":"parallel-node-2"}`), sub2.ID)
	sub1.ManagedNodes().Store(hash1, []string{"tag1"})
	sub2.ManagedNodes().Store(hash2, []string{"tag2"})

	entry1, ok := pool.GetEntry(hash1)
	if !ok {
		t.Fatal("entry1 not found")
	}
	entry2, ok := pool.GetEntry(hash2)
	if !ok {
		t.Fatal("entry2 not found")
	}

	pastTime := time.Now().Add(-1 * time.Hour).UnixNano()
	entry1.CircuitOpenSince.Store(pastTime)
	entry2.CircuitOpenSince.Store(pastTime)

	releaseHook := make(chan struct{})
	allStarted := make(chan struct{})
	var started atomic.Int32

	cleaner := NewEphemeralCleaner(subMgr, pool, func() time.Duration { return 30 * time.Second })
	done := make(chan struct{})
	go func() {
		cleaner.sweepWithHook(func() {
			if started.Add(1) == 2 {
				close(allStarted)
			}
			<-releaseHook
		})
		close(done)
	}()

	select {
	case <-allStarted:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected ephemeral subscription sweeps to run in parallel")
	}

	select {
	case <-done:
		t.Fatal("sweepWithHook should wait for in-flight subscription sweeps")
	default:
	}

	close(releaseHook)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sweepWithHook did not finish after release")
	}

	if got := started.Load(); got != 2 {
		t.Fatalf("expected hook to run for 2 subscriptions, got %d", got)
	}
}
