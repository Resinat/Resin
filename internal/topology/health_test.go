package topology

import (
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/subscription"
	"github.com/resin-proxy/resin/internal/testutil"
)

func newHealthTestPool(maxFailures int) (*GlobalNodePool, *SubscriptionManager) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "url", true, false)
	subMgr.Register(sub)

	pool := NewGlobalNodePool(PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(addr netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return maxFailures },
	})
	return pool, subMgr
}

func addTestNode(pool *GlobalNodePool, sub *subscription.Subscription, raw string) node.Hash {
	h := node.HashFromRawOptions([]byte(raw))
	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"node"})
	sub.SwapManagedNodes(mn)
	pool.AddNodeFromSub(h, []byte(raw), "s1")
	return h
}

// --- RecordResult tests ---

func TestRecordResult_CircuitBreak(t *testing.T) {
	pool, subMgr := newHealthTestPool(3) // break after 3 failures
	sub := subMgr.Lookup("s1")
	h := addTestNode(pool, sub, `{"type":"ss","n":"1"}`)

	// 2 failures — not yet broken.
	pool.RecordResult(h, false)
	pool.RecordResult(h, false)
	entry, _ := pool.GetEntry(h)
	if entry.IsCircuitOpen() {
		t.Fatal("should not be circuit-open after 2 failures")
	}

	// 3rd failure → circuit opens.
	pool.RecordResult(h, false)
	if !entry.IsCircuitOpen() {
		t.Fatal("should be circuit-open after 3 failures")
	}
	if entry.FailureCount.Load() != 3 {
		t.Fatalf("expected FailureCount=3, got %d", entry.FailureCount.Load())
	}
}

func TestRecordResult_Recovery(t *testing.T) {
	pool, subMgr := newHealthTestPool(2)
	sub := subMgr.Lookup("s1")
	h := addTestNode(pool, sub, `{"type":"ss","n":"2"}`)

	pool.RecordResult(h, false)
	pool.RecordResult(h, false)
	entry, _ := pool.GetEntry(h)
	if !entry.IsCircuitOpen() {
		t.Fatal("should be circuit open")
	}

	// Success → resets.
	pool.RecordResult(h, true)
	if entry.IsCircuitOpen() {
		t.Fatal("should not be circuit-open after success")
	}
	if entry.FailureCount.Load() != 0 {
		t.Fatalf("expected FailureCount=0, got %d", entry.FailureCount.Load())
	}
}

func TestRecordResult_MaxConsecutiveFailuresPulled(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "url", true, false)
	subMgr.Register(sub)

	var maxFailures atomic.Int64
	maxFailures.Store(3)

	pool := NewGlobalNodePool(PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(addr netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return int(maxFailures.Load()) },
	})

	h := addTestNode(pool, sub, `{"type":"ss","n":"pull-threshold"}`)
	entry, _ := pool.GetEntry(h)

	pool.RecordResult(h, false)
	if entry.IsCircuitOpen() {
		t.Fatal("should not be circuit-open after first failure")
	}

	// Lower threshold dynamically. Next failure should open circuit.
	maxFailures.Store(2)
	pool.RecordResult(h, false)
	if !entry.IsCircuitOpen() {
		t.Fatal("should be circuit-open after threshold shrinks")
	}
}

func TestRecordResult_DynamicCallback_OnActualChange(t *testing.T) {
	var count atomic.Int32
	pool := NewGlobalNodePool(PoolConfig{
		SubLookup:              NewSubscriptionManager().Lookup,
		GeoLookup:              func(addr netip.Addr) string { return "" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 5 },
		OnNodeDynamicChanged:   func(hash node.Hash) { count.Add(1) },
	})

	raw := `{"type":"ss","n":"cb"}`
	h := node.HashFromRawOptions([]byte(raw))
	pool.AddNodeFromSub(h, []byte(raw), "s1")

	pool.RecordResult(h, true)
	pool.RecordResult(h, false)
	pool.RecordResult(h, true)

	// First success is a no-op (already healthy), then failure and recovery mutate state.
	if count.Load() != 2 {
		t.Fatalf("expected 2 callbacks, got %d", count.Load())
	}
}

func TestRecordResult_CircuitBreak_RemovesFromView(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "url", true, false)
	subMgr.Register(sub)

	pool := NewGlobalNodePool(PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(addr netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 2 },
	})
	plat := platform.NewPlatform("p1", "Test", nil, nil)
	pool.RegisterPlatform(plat)

	h := addTestNode(pool, sub, `{"type":"ss","n":"view"}`)

	// Make entry fully routable.
	entry, _ := pool.GetEntry(h)
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))
	pool.RebuildAllPlatforms()

	if plat.View().Size() != 1 {
		t.Fatal("node should be in view initially")
	}

	// Circuit break → remove from view.
	pool.RecordResult(h, false)
	pool.RecordResult(h, false)
	if plat.View().Size() != 0 {
		t.Fatal("circuit-broken node should be removed from view")
	}

	// Recover → back in view.
	pool.RecordResult(h, true)
	if plat.View().Size() != 1 {
		t.Fatal("recovered node should be back in view")
	}
}

// --- RecordLatency tests ---

func TestRecordLatency_NormalizesDomain(t *testing.T) {
	pool, subMgr := newHealthTestPool(5)
	sub := subMgr.Lookup("s1")
	h := addTestNode(pool, sub, `{"type":"ss","n":"lat"}`)

	// Pass raw target with subdomain+port — should normalize to eTLD+1.
	latency := 100 * time.Millisecond
	pool.RecordLatency(h, "www.example.com:443", &latency)

	entry, _ := pool.GetEntry(h)
	stats, ok := entry.LatencyTable.GetDomainStats("example.com")
	if !ok {
		t.Fatal("should find stats for normalized domain 'example.com'")
	}
	if stats.Ewma != 100*time.Millisecond {
		t.Fatalf("expected 100ms, got %v", stats.Ewma)
	}
}

func TestRecordLatency_FirstRecord_PlatformDirty(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "url", true, false)
	subMgr.Register(sub)

	var latencyCBCount atomic.Int32
	pool := NewGlobalNodePool(PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(addr netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		OnNodeLatencyChanged:   func(hash node.Hash, domain string) { latencyCBCount.Add(1) },
	})

	h := addTestNode(pool, sub, `{"type":"ss","n":"first"}`)

	// First record → wasEmpty=true → platform dirty.
	latency := 50 * time.Millisecond
	pool.RecordLatency(h, "example.com", &latency)
	if latencyCBCount.Load() != 1 {
		t.Fatalf("expected 1 latency callback, got %d", latencyCBCount.Load())
	}
}

func TestRecordLatency_AttemptOnly_UpdatesAttemptTimestamps(t *testing.T) {
	var dynamicCBCount atomic.Int32
	pool := NewGlobalNodePool(PoolConfig{
		SubLookup:              NewSubscriptionManager().Lookup,
		GeoLookup:              func(netip.Addr) string { return "" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyAuthorities:     func() []string { return []string{"example.com"} },
		OnNodeDynamicChanged:   func(hash node.Hash) { dynamicCBCount.Add(1) },
	})

	raw := `{"type":"ss","n":"attempt-only"}`
	h := node.HashFromRawOptions([]byte(raw))
	pool.AddNodeFromSub(h, []byte(raw), "s1")

	entry, _ := pool.GetEntry(h)
	if entry.LastLatencyProbeAttempt.Load() != 0 || entry.LastAuthorityLatencyProbeAttempt.Load() != 0 {
		t.Fatalf("attempt timestamps should start at 0: %+v", entry)
	}

	pool.RecordLatency(h, "www.example.com:443", nil)

	if entry.LastLatencyProbeAttempt.Load() == 0 {
		t.Fatal("LastLatencyProbeAttempt should be updated")
	}
	if entry.LastAuthorityLatencyProbeAttempt.Load() == 0 {
		t.Fatal("LastAuthorityLatencyProbeAttempt should be updated for authority domain")
	}
	if entry.HasLatency() {
		t.Fatal("attempt-only RecordLatency(nil) must not write latency sample")
	}
	if dynamicCBCount.Load() != 1 {
		t.Fatalf("expected 1 dynamic callback, got %d", dynamicCBCount.Load())
	}
}

// --- UpdateNodeEgressIP tests ---

func TestUpdateNodeEgressIP_Change(t *testing.T) {
	var dynamicCount atomic.Int32
	pool := NewGlobalNodePool(PoolConfig{
		SubLookup:              NewSubscriptionManager().Lookup,
		GeoLookup:              func(addr netip.Addr) string { return "" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		OnNodeDynamicChanged:   func(hash node.Hash) { dynamicCount.Add(1) },
	})

	raw := `{"type":"ss","n":"egress"}`
	h := node.HashFromRawOptions([]byte(raw))
	pool.AddNodeFromSub(h, []byte(raw), "s1")

	ip1 := netip.MustParseAddr("1.2.3.4")
	pool.UpdateNodeEgressIP(h, &ip1, nil)
	if dynamicCount.Load() != 1 {
		t.Fatalf("expected 1 callback on first IP set, got %d", dynamicCount.Load())
	}

	entry, _ := pool.GetEntry(h)
	if entry.GetEgressIP() != ip1 {
		t.Fatalf("expected %v, got %v", ip1, entry.GetEgressIP())
	}

	// Same IP still updates probe-attempt timestamp.
	pool.UpdateNodeEgressIP(h, &ip1, nil)
	if dynamicCount.Load() != 2 {
		t.Fatalf("expected callback on same IP attempt, got %d", dynamicCount.Load())
	}

	// Different IP → callback.
	ip2 := netip.MustParseAddr("5.6.7.8")
	pool.UpdateNodeEgressIP(h, &ip2, nil)
	if dynamicCount.Load() != 3 {
		t.Fatalf("expected 3 callbacks after IP change, got %d", dynamicCount.Load())
	}
}

func TestUpdateNodeEgressIP_LocStateMachine(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "url", true, false)
	subMgr.Register(sub)

	pool := NewGlobalNodePool(PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(_ netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	plat := platform.NewPlatform("p1", "JP-Only", nil, []string{"jp"})
	pool.RegisterPlatform(plat)

	h := addTestNode(pool, sub, `{"type":"ss","n":"egress-loc"}`)
	entry, _ := pool.GetEntry(h)
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        30 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)

	ip := netip.MustParseAddr("1.2.3.4")
	locJP := "jp"
	pool.UpdateNodeEgressIP(h, &ip, &locJP)
	if got := entry.GetEgressRegion(); got != "jp" {
		t.Fatalf("egress region: got %q, want %q", got, "jp")
	}
	if plat.View().Size() != 1 {
		t.Fatal("node should be routable with explicit jp region")
	}

	locUS := "us"
	pool.UpdateNodeEgressIP(h, &ip, &locUS)
	if got := entry.GetEgressRegion(); got != "us" {
		t.Fatalf("egress region: got %q, want %q", got, "us")
	}
	if plat.View().Size() != 0 {
		t.Fatal("same IP but changed region should trigger platform re-evaluation")
	}

	// ip unchanged + loc=nil => keep region.
	pool.UpdateNodeEgressIP(h, &ip, nil)
	if got := entry.GetEgressRegion(); got != "us" {
		t.Fatalf("egress region should keep when ip unchanged and loc=nil: got %q", got)
	}

	// ip=nil + loc=nil => keep both ip and region.
	pool.UpdateNodeEgressIP(h, nil, nil)
	if got := entry.GetEgressRegion(); got != "us" {
		t.Fatalf("egress region should remain unchanged on nil/nil attempt: got %q", got)
	}
	if got := entry.GetEgressIP(); got != ip {
		t.Fatalf("egress IP should remain on attempt-only update: got %v, want %v", got, ip)
	}

	// ip changed + loc=nil => clear region.
	ip2 := netip.MustParseAddr("5.6.7.8")
	pool.UpdateNodeEgressIP(h, &ip2, nil)
	if got := entry.GetEgressRegion(); got != "" {
		t.Fatalf("egress region should clear when ip changed and loc=nil: got %q", got)
	}
	if got := entry.GetEgressIP(); got != ip2 {
		t.Fatalf("egress IP should update on ip change: got %v, want %v", got, ip2)
	}
}
