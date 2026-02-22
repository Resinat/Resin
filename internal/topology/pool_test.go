package topology

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
)

func newTestPool(subMgr *SubscriptionManager) *GlobalNodePool {
	return NewGlobalNodePool(PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(addr netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
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
	ob := testutil.NewNoopOutbound()
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

func TestPool_NotifyNodeDirty_UpdatesPlatformsInParallel(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	subMgr.Register(sub)

	releaseGeoLookup := make(chan struct{})
	allGeoLookupStarted := make(chan struct{})
	var geoLookupCalls atomic.Int32

	pool := NewGlobalNodePool(PoolConfig{
		SubLookup: subMgr.Lookup,
		GeoLookup: func(addr netip.Addr) string {
			if geoLookupCalls.Add(1) == 2 {
				close(allGeoLookupStarted)
			}
			<-releaseGeoLookup
			return "us"
		},
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	plat1 := platform.NewPlatform("p1", "P1", nil, []string{"us"})
	plat2 := platform.NewPlatform("p2", "P2", nil, []string{"us"})
	pool.RegisterPlatform(plat1)
	pool.RegisterPlatform(plat2)

	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)
	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"node-1"})
	sub.SwapManagedNodes(mn)

	pool.AddNodeFromSub(h, raw, "s1")
	entry, ok := pool.GetEntry(h)
	if !ok {
		t.Fatal("entry not found")
	}
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))

	done := make(chan struct{})
	go func() {
		pool.NotifyNodeDirty(h)
		close(done)
	}()

	select {
	case <-allGeoLookupStarted:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected platform dirty notifications to run in parallel")
	}

	select {
	case <-done:
		t.Fatal("NotifyNodeDirty should wait for in-flight platform notifications")
	default:
	}

	close(releaseGeoLookup)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("NotifyNodeDirty did not finish after releasing geo lookups")
	}

	if got := geoLookupCalls.Load(); got != 2 {
		t.Fatalf("expected 2 geo lookup calls, got %d", got)
	}
}

func TestPool_RebuildAllPlatforms_UpdatesInParallel(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	subMgr.Register(sub)

	releaseGeoLookup := make(chan struct{})
	allGeoLookupStarted := make(chan struct{})
	var geoLookupCalls atomic.Int32

	pool := NewGlobalNodePool(PoolConfig{
		SubLookup: subMgr.Lookup,
		GeoLookup: func(addr netip.Addr) string {
			if geoLookupCalls.Add(1) == 2 {
				close(allGeoLookupStarted)
			}
			<-releaseGeoLookup
			return "us"
		},
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)
	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"node-1"})
	sub.SwapManagedNodes(mn)

	pool.AddNodeFromSub(h, raw, "s1")
	entry, ok := pool.GetEntry(h)
	if !ok {
		t.Fatal("entry not found")
	}
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))

	plat1 := platform.NewPlatform("p1", "P1", nil, []string{"us"})
	plat2 := platform.NewPlatform("p2", "P2", nil, []string{"us"})
	pool.RegisterPlatform(plat1)
	pool.RegisterPlatform(plat2)

	done := make(chan struct{})
	go func() {
		pool.RebuildAllPlatforms()
		close(done)
	}()

	select {
	case <-allGeoLookupStarted:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected platform rebuilds to run in parallel")
	}

	select {
	case <-done:
		t.Fatal("RebuildAllPlatforms should wait for in-flight rebuilds")
	default:
	}

	close(releaseGeoLookup)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RebuildAllPlatforms did not finish after releasing geo lookups")
	}

	if got := geoLookupCalls.Load(); got != 2 {
		t.Fatalf("expected 2 geo lookup calls, got %d", got)
	}
	if !plat1.View().Contains(h) || !plat2.View().Contains(h) {
		t.Fatal("rebuild should populate both platform views")
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
		ob := testutil.NewNoopOutbound()
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

func TestPool_PlatformLookupByIDAndName(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	plat := platform.NewPlatform("p-lookup", "LookupPlat", nil, nil)
	pool.RegisterPlatform(plat)

	gotByID, ok := pool.GetPlatform("p-lookup")
	if !ok || gotByID != plat {
		t.Fatal("GetPlatform should return registered platform by ID")
	}

	gotByName, ok := pool.GetPlatformByName("LookupPlat")
	if !ok || gotByName != plat {
		t.Fatal("GetPlatformByName should return registered platform by name")
	}
}

func TestPool_ResolveNodeDisplayTag_EarliestSubscriptionThenMinTag(t *testing.T) {
	subMgr := NewSubscriptionManager()

	older := subscription.NewSubscription("sub-old", "Z-Provider", "url", true, false)
	older.CreatedAtNs = 100
	older.SetEnabled(false) // display-tag rule should not depend on enabled flag.

	newer := subscription.NewSubscription("sub-new", "A-Provider", "url", true, false)
	newer.CreatedAtNs = 200

	subMgr.Register(older)
	subMgr.Register(newer)

	pool := newTestPool(subMgr)
	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)

	oldManaged := xsync.NewMap[node.Hash, []string]()
	oldManaged.Store(h, []string{"zz", "aa"})
	older.SwapManagedNodes(oldManaged)

	newManaged := xsync.NewMap[node.Hash, []string]()
	newManaged.Store(h, []string{"00"})
	newer.SwapManagedNodes(newManaged)

	pool.AddNodeFromSub(h, raw, older.ID)
	pool.AddNodeFromSub(h, raw, newer.ID)

	got := pool.ResolveNodeDisplayTag(h)
	want := "Z-Provider/aa"
	if got != want {
		t.Fatalf("ResolveNodeDisplayTag = %q, want %q", got, want)
	}

	if v := pool.ResolveNodeDisplayTag(node.Zero); v != "" {
		t.Fatalf("ResolveNodeDisplayTag(unknown) = %q, want empty", v)
	}
}

func TestPool_RangePlatforms_UsesSnapshotAndDoesNotDeadlock(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	plat1 := platform.NewPlatform("p-range-1", "Range-1", nil, nil)
	plat2 := platform.NewPlatform("p-range-2", "Range-2", nil, nil)
	pool.RegisterPlatform(plat1)
	pool.RegisterPlatform(plat2)

	done := make(chan struct{})
	go func() {
		pool.RangePlatforms(func(_ *platform.Platform) bool {
			// Mutating during range should not deadlock because RangePlatforms
			// iterates on a snapshot.
			pool.UnregisterPlatform("p-range-2")
			return true
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RangePlatforms deadlocked while unregistering during callback")
	}

	if _, ok := pool.GetPlatform("p-range-2"); ok {
		t.Fatal("platform should be removed after unregister")
	}
}

func TestPool_ReplacePlatform_Success(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	oldPlat := platform.NewPlatform("p-replace", "OldName", nil, nil)
	pool.RegisterPlatform(oldPlat)

	nextPlat := platform.NewPlatform("p-replace", "NewName", nil, nil)
	if err := pool.ReplacePlatform(nextPlat); err != nil {
		t.Fatalf("ReplacePlatform error: %v", err)
	}

	gotByID, ok := pool.GetPlatform("p-replace")
	if !ok || gotByID != nextPlat {
		t.Fatal("GetPlatform should return replaced platform by ID")
	}

	if _, ok := pool.GetPlatformByName("OldName"); ok {
		t.Fatal("old name mapping should be removed")
	}
	gotByName, ok := pool.GetPlatformByName("NewName")
	if !ok || gotByName != nextPlat {
		t.Fatal("new name mapping should point to replaced platform")
	}
}

func TestPool_ReplacePlatform_NameConflict(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	platA := platform.NewPlatform("p-a", "A", nil, nil)
	platB := platform.NewPlatform("p-b", "B", nil, nil)
	pool.RegisterPlatform(platA)
	pool.RegisterPlatform(platB)

	conflict := platform.NewPlatform("p-a", "B", nil, nil)
	err := pool.ReplacePlatform(conflict)
	if err == nil || !errors.Is(err, ErrPlatformNameConflict) {
		t.Fatalf("ReplacePlatform error = %v, want ErrPlatformNameConflict", err)
	}

	gotA, ok := pool.GetPlatform("p-a")
	if !ok || gotA != platA {
		t.Fatal("platform p-a should remain unchanged on conflict")
	}
	gotByNameA, ok := pool.GetPlatformByName("A")
	if !ok || gotByNameA != platA {
		t.Fatal("name mapping A should remain unchanged on conflict")
	}
	gotByNameB, ok := pool.GetPlatformByName("B")
	if !ok || gotByNameB != platB {
		t.Fatal("name mapping B should remain unchanged on conflict")
	}
}

func TestPool_ReplacePlatform_NotRegistered(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	err := pool.ReplacePlatform(platform.NewPlatform("missing", "Nope", nil, nil))
	if err == nil || !errors.Is(err, ErrPlatformNotRegistered) {
		t.Fatalf("ReplacePlatform error = %v, want ErrPlatformNotRegistered", err)
	}
}

func TestPool_ReplacePlatform_RebuildsViewBeforePublish(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Provider", "url", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	oldPlat := platform.NewPlatform("p-rebuild", "Old", nil, nil)
	pool.RegisterPlatform(oldPlat)

	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)

	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"us-node"})
	sub.SwapManagedNodes(mn)

	pool.AddNodeFromSub(h, raw, "s1")
	entry, _ := pool.GetEntry(h)
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))
	pool.NotifyNodeDirty(h)

	// New platform requires "us" in tag. If ReplacePlatform skipped rebuild,
	// its view would remain empty here.
	nextPlat := platform.NewPlatform(
		"p-rebuild",
		"New",
		[]*regexp.Regexp{regexp.MustCompile("us")},
		nil,
	)
	if err := pool.ReplacePlatform(nextPlat); err != nil {
		t.Fatalf("ReplacePlatform error: %v", err)
	}

	got, ok := pool.GetPlatform("p-rebuild")
	if !ok || got != nextPlat {
		t.Fatal("expected replaced platform by ID")
	}
	if !got.View().Contains(h) {
		t.Fatal("replaced platform view should include routable us-node")
	}
}

// --- Subscription operation-lock tests ---

func TestSubscription_WithOpLock(t *testing.T) {
	mgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	mgr.Register(sub)

	// WithOpLock should serialize.
	counter := 0
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub.WithOpLock(func() {
				counter++
			})
		}()
	}
	wg.Wait()

	if counter != 100 {
		t.Fatalf("expected 100, got %d (serialization broken)", counter)
	}
}

func TestSubscription_WithOpLockAfterUnregister(t *testing.T) {
	mgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	mgr.Register(sub)

	firstEntered := make(chan struct{})
	firstRelease := make(chan struct{})
	secondEntered := make(chan struct{})

	var firstWG sync.WaitGroup
	firstWG.Add(1)
	go func() {
		defer firstWG.Done()
		sub.WithOpLock(func() {
			close(firstEntered)
			// Simulate delete path that unregisters while holding the lock.
			mgr.Unregister("s1")
			<-firstRelease
		})
	}()

	<-firstEntered

	go sub.WithOpLock(func() {
		close(secondEntered)
	})

	select {
	case <-secondEntered:
		close(firstRelease)
		firstWG.Wait()
		t.Fatal("second WithOpLock entered before first lock holder exited")
	case <-time.After(100 * time.Millisecond):
		// expected: second goroutine must block on the same lock
	}

	close(firstRelease)
	firstWG.Wait()

	select {
	case <-secondEntered:
		// expected
	case <-time.After(time.Second):
		t.Fatal("second WithOpLock did not enter after first lock holder exited")
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

	cleaner := NewEphemeralCleaner(subMgr, pool, func() time.Duration { return 1 * time.Minute })
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

	cleaner := NewEphemeralCleaner(subMgr, pool, func() time.Duration { return 1 * time.Minute })
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

	cleaner := NewEphemeralCleaner(subMgr, pool, func() time.Duration { return 1 * time.Minute })
	cleaner.sweep()

	// Should NOT be evicted yet.
	if pool.Size() != 1 {
		t.Fatal("recently circuit-broken node should not be evicted yet")
	}
}
