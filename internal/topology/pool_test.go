package topology

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/subscription"
)

func newTestPool(subMgr *SubscriptionManager) *GlobalNodePool {
	return NewGlobalNodePool(PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(addr netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
	})
}

// --- Pool tests ---

func TestPool_AddNodeFromSub_Idempotent(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)

	// Set up managed nodes so MatchRegexs can see them.
	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"us-node"})
	sub.SwapManagedNodes(mn)

	// Add twice — should be idempotent.
	pool.AddNodeFromSub(h, raw, "s1")
	pool.AddNodeFromSub(h, raw, "s1")

	if pool.Size() != 1 {
		t.Fatalf("expected 1 node, got %d", pool.Size())
	}

	entry, ok := pool.GetEntry(h)
	if !ok {
		t.Fatal("entry not found")
	}
	if entry.SubscriptionCount() != 1 {
		t.Fatalf("expected 1 sub ref, got %d", entry.SubscriptionCount())
	}
}

func TestPool_RemoveNodeFromSub_Idempotent(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)
	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)

	// Remove nonexistent — should not panic.
	pool.RemoveNodeFromSub(h, "s1")

	pool.AddNodeFromSub(h, raw, "s1")
	pool.RemoveNodeFromSub(h, "s1")
	pool.RemoveNodeFromSub(h, "s1") // idempotent

	if pool.Size() != 0 {
		t.Fatalf("expected 0 nodes, got %d", pool.Size())
	}
}

func TestPool_CrossSubDedup(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub1 := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	sub2 := subscription.NewSubscription("s2", "Sub2", "url", true, false)
	subMgr.Register(sub1)
	subMgr.Register(sub2)

	pool := newTestPool(subMgr)
	raw := json.RawMessage(`{"type":"ss","server":"same"}`)
	h := node.HashFromRawOptions(raw)

	pool.AddNodeFromSub(h, raw, "s1")
	pool.AddNodeFromSub(h, raw, "s2")

	if pool.Size() != 1 {
		t.Fatalf("expected 1 deduped node, got %d", pool.Size())
	}

	entry, _ := pool.GetEntry(h)
	if entry.SubscriptionCount() != 2 {
		t.Fatalf("expected 2 sub refs, got %d", entry.SubscriptionCount())
	}

	// Remove one sub ref — node should remain.
	pool.RemoveNodeFromSub(h, "s1")
	if pool.Size() != 1 {
		t.Fatal("node should remain after removing one sub ref")
	}

	// Remove last ref — node should be deleted.
	pool.RemoveNodeFromSub(h, "s2")
	if pool.Size() != 0 {
		t.Fatal("node should be deleted when all refs removed")
	}
}

func TestPool_ConcurrentAddRemove(t *testing.T) {
	subMgr := NewSubscriptionManager()
	for i := 0; i < 10; i++ {
		sub := subscription.NewSubscription(fmt.Sprintf("s%d", i), fmt.Sprintf("Sub%d", i), "url", true, false)
		subMgr.Register(sub)
	}

	pool := newTestPool(subMgr)
	hashes := make([]node.Hash, 100)
	raws := make([]json.RawMessage, 100)
	for i := range hashes {
		raw := json.RawMessage(fmt.Sprintf(`{"type":"ss","n":%d}`, i))
		hashes[i] = node.HashFromRawOptions(raw)
		raws[i] = raw
	}

	var wg sync.WaitGroup
	// 10 goroutines add nodes concurrently.
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(subIdx int) {
			defer wg.Done()
			subID := fmt.Sprintf("s%d", subIdx)
			for i := subIdx * 10; i < (subIdx+1)*10; i++ {
				pool.AddNodeFromSub(hashes[i], raws[i], subID)
			}
		}(g)
	}
	wg.Wait()

	if pool.Size() != 100 {
		t.Fatalf("expected 100 nodes, got %d", pool.Size())
	}

	// Concurrently remove all.
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(subIdx int) {
			defer wg.Done()
			subID := fmt.Sprintf("s%d", subIdx)
			for i := subIdx * 10; i < (subIdx+1)*10; i++ {
				pool.RemoveNodeFromSub(hashes[i], subID)
			}
		}(g)
	}
	wg.Wait()

	if pool.Size() != 0 {
		t.Fatalf("expected 0 nodes after concurrent remove, got %d", pool.Size())
	}
}

func TestPool_PlatformNotifyOnAddRemove(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)

	// Create a platform with no filters (everything passes regex/region checks).
	plat := platform.NewPlatform("p1", "TestPlat", nil, nil)
	pool.RegisterPlatform(plat)

	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)

	// Set managed nodes for sub.
	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"node-1"})
	sub.SwapManagedNodes(mn)

	// Create entry with all conditions met for routing.
	pool.AddNodeFromSub(h, raw, "s1")

	// The node won't be in the view yet because it has no latency/outbound.
	if plat.View().Size() != 0 {
		t.Fatal("new node without latency/outbound should not be in view")
	}

	// Set latency+outbound on entry, then re-trigger dirty.
	entry, _ := pool.GetEntry(h)
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	var ob any = "mock"
	entry.Outbound.Store(&ob)
	entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))

	// Re-add triggers NotifyDirty.
	pool.AddNodeFromSub(h, raw, "s1")
	if plat.View().Size() != 1 {
		t.Fatal("node with all conditions should be in view after re-add")
	}

	// Remove → should leave view.
	pool.RemoveNodeFromSub(h, "s1")
	if plat.View().Size() != 0 {
		t.Fatal("deleted node should be removed from view")
	}
}

func TestPool_RegexFilteredPlatform(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Provider", "url", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)

	// Platform with "us" regex filter.
	plat := platform.NewPlatform("p1", "US-Only", []*regexp.Regexp{regexp.MustCompile("us")}, nil)
	pool.RegisterPlatform(plat)

	h1 := node.HashFromRawOptions([]byte(`{"type":"ss","n":"us"}`))
	h2 := node.HashFromRawOptions([]byte(`{"type":"ss","n":"jp"}`))

	// Setup managedNodes with appropriate tags.
	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h1, []string{"us-node"})
	mn.Store(h2, []string{"jp-node"})
	sub.SwapManagedNodes(mn)

	// Make both fully routable.
	for _, h := range []node.Hash{h1, h2} {
		pool.AddNodeFromSub(h, nil, "s1")
		entry, _ := pool.GetEntry(h)
		entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
			Ewma:        100 * time.Millisecond,
			LastUpdated: time.Now(),
		})
		var ob any = "mock"
		entry.Outbound.Store(&ob)
		entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))
		// Re-trigger dirty to pick up latency/outbound.
		pool.AddNodeFromSub(h, nil, "s1")
	}

	// Only us-node should be in view ("Provider/us-node" matches "us").
	if plat.View().Size() != 1 {
		t.Fatalf("expected 1 node in filtered view, got %d", plat.View().Size())
	}
	if !plat.View().Contains(h1) {
		t.Fatal("us-node should be in view")
	}
	if plat.View().Contains(h2) {
		t.Fatal("jp-node should NOT be in view")
	}
}

// --- SubscriptionManager tests ---

func TestSubscriptionManager_WithSubLock(t *testing.T) {
	mgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	mgr.Register(sub)

	// WithSubLock should serialize.
	counter := 0
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.WithSubLock("s1", func() {
				counter++
			})
		}()
	}
	wg.Wait()

	if counter != 100 {
		t.Fatalf("expected 100, got %d (serialization broken)", counter)
	}
}

// --- Ephemeral Cleaner tests ---

func TestEphemeralCleaner_EvictsCircuitBroken(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "EphSub", "url", true, true) // ephemeral
	subMgr.Register(sub)

	pool := newTestPool(subMgr)

	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)

	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"node-1"})
	sub.SwapManagedNodes(mn)

	pool.AddNodeFromSub(h, raw, "s1")

	// Circuit-break the node for longer than evict delay.
	entry, _ := pool.GetEntry(h)
	entry.CircuitOpenSince.Store(time.Now().Add(-2 * time.Minute).UnixNano())

	cleaner := NewEphemeralCleaner(subMgr, pool, 1*time.Minute)
	cleaner.sweep()

	// Node should be evicted.
	if pool.Size() != 0 {
		t.Fatal("circuit-broken node should be evicted from ephemeral sub")
	}

	// ManagedNodes should be empty.
	count := 0
	sub.ManagedNodes().Range(func(_ node.Hash, _ []string) bool {
		count++
		return true
	})
	if count != 0 {
		t.Fatalf("expected 0 managed nodes, got %d", count)
	}
}

func TestEphemeralCleaner_SkipsNonEphemeral(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "RegularSub", "url", true, false) // NOT ephemeral
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)

	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"node-1"})
	sub.SwapManagedNodes(mn)

	pool.AddNodeFromSub(h, raw, "s1")

	entry, _ := pool.GetEntry(h)
	entry.CircuitOpenSince.Store(time.Now().Add(-2 * time.Minute).UnixNano())

	cleaner := NewEphemeralCleaner(subMgr, pool, 1*time.Minute)
	cleaner.sweep()

	// Node should NOT be evicted since sub is not ephemeral.
	if pool.Size() != 1 {
		t.Fatal("non-ephemeral sub nodes should not be evicted")
	}
}

func TestEphemeralCleaner_SkipsRecentCircuitBreak(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "EphSub", "url", true, true)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)

	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"node-1"})
	sub.SwapManagedNodes(mn)

	pool.AddNodeFromSub(h, raw, "s1")

	// Circuit-break recently (less than evict delay).
	entry, _ := pool.GetEntry(h)
	entry.CircuitOpenSince.Store(time.Now().Add(-10 * time.Second).UnixNano())

	cleaner := NewEphemeralCleaner(subMgr, pool, 1*time.Minute)
	cleaner.sweep()

	// Should NOT be evicted yet.
	if pool.Size() != 1 {
		t.Fatal("recently circuit-broken node should not be evicted yet")
	}
}
