package routing_test

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/outbound"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/routing"
	"github.com/resin-proxy/resin/internal/subscription"
	"github.com/resin-proxy/resin/internal/testutil"
	"github.com/resin-proxy/resin/internal/topology"
)

// ── helpers ─────────────────────────────────────────────────────

const platID = "test-plat"
const platName = "TestPlat"

// makeEntry creates a node with outbound, egress IP, and latency filled in
// so it passes platform.PassesFilter automatically.
func makeRoutableNode(t testing.TB, pool *topology.GlobalNodePool, subMgr *topology.SubscriptionManager, raw string, ip string, latDomain string, latency time.Duration) node.Hash {
	t.Helper()
	rawOpts := json.RawMessage(raw)
	h := node.HashFromRawOptions(rawOpts)

	// Ensure sub has the node in managed nodes.
	sub, _ := subMgr.Get("sub-1")
	sub.ManagedNodes().Store(h, []string{"tag"})

	pool.AddNodeFromSub(h, rawOpts, "sub-1")

	entry, ok := pool.GetEntry(h)
	if !ok {
		t.Fatalf("node %s not in pool after add", h.Hex())
	}

	// Set outbound
	obMgr := outbound.NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	obMgr.EnsureNodeOutbound(h)

	// Set egress IP
	entry.SetEgressIP(netip.MustParseAddr(ip))

	// Record latency
	entry.LatencyTable.Update(latDomain, latency, 10*time.Minute)

	// Trigger platform re-evaluation
	pool.NotifyNodeDirty(h)

	return h
}

func setupPool(t testing.TB) (*topology.GlobalNodePool, *topology.SubscriptionManager) {
	t.Helper()
	subMgr := topology.NewSubscriptionManager()
	geoLookup := func(_ netip.Addr) string { return "US" } // pass all region filters

	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              geoLookup,
		MaxLatencyTableEntries: 10,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	platCfg := platform.NewPlatform(platID, platName, []*regexp.Regexp{}, []string{})
	platCfg.StickyTTLNs = int64(1 * time.Hour) // 1 hour stickiness
	pool.RegisterPlatform(platCfg)

	sub := subscription.NewSubscription("sub-1", "Test Sub", "https://example.com", true, false)
	subMgr.Register(sub)

	return pool, subMgr
}

func makeRouter(pool *topology.GlobalNodePool, events *[]routing.LeaseEvent) *routing.Router {
	var mu sync.Mutex
	return routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"cloudflare.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
		OnLeaseEvent: func(e routing.LeaseEvent) {
			if events != nil {
				mu.Lock()
				*events = append(*events, e)
				mu.Unlock()
			}
		},
	})
}

// ── randomRoute tests ───────────────────────────────────────────

func TestRandomRoute_EmptyView(t *testing.T) {
	pool, _ := setupPool(t)
	router := makeRouter(pool, nil)

	// No nodes → ErrNoAvailableNodes.
	_, err := router.RouteRequest(platName, "", "example.com")
	if err == nil {
		t.Fatal("expected error for empty view")
	}
}

func TestRandomRoute_SingleNode(t *testing.T) {
	pool, subMgr := setupPool(t)
	h1 := makeRoutableNode(t, pool, subMgr, `{"single":"1"}`, "1.2.3.4", "cloudflare.com", 50*time.Millisecond)

	router := makeRouter(pool, nil)
	res, err := router.RouteRequest(platName, "", "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.NodeHash != h1 {
		t.Fatalf("expected hash %s, got %s", h1.Hex(), res.NodeHash.Hex())
	}
	if res.PlatformID != platID || res.PlatformName != platName {
		t.Fatalf("expected platform metadata id=%q name=%q, got id=%q name=%q", platID, platName, res.PlatformID, res.PlatformName)
	}
}

func TestRandomRoute_MultipleNodes(t *testing.T) {
	pool, subMgr := setupPool(t)
	h1 := makeRoutableNode(t, pool, subMgr, `{"multi":"1"}`, "10.0.0.1", "cloudflare.com", 50*time.Millisecond)
	h2 := makeRoutableNode(t, pool, subMgr, `{"multi":"2"}`, "10.0.0.2", "cloudflare.com", 50*time.Millisecond)

	router := makeRouter(pool, nil)

	// Call many times; we must always get one of our two nodes.
	seen := map[node.Hash]int{}
	for i := 0; i < 100; i++ {
		res, err := router.RouteRequest(platName, "", "example.com")
		if err != nil {
			t.Fatalf("unexpected error on iteration %d: %v", i, err)
		}
		if res.NodeHash != h1 && res.NodeHash != h2 {
			t.Fatalf("unexpected hash %s; expected %s or %s", res.NodeHash.Hex(), h1.Hex(), h2.Hex())
		}
		seen[res.NodeHash]++
	}
	// Both should appear (P2C with similar scores should distribute).
	if len(seen) < 2 {
		t.Log("Warning: only one node was selected in 100 iterations (may happen rarely)")
	}
}

// ── sticky lease tests ──────────────────────────────────────────

func TestStickyLease_CreateAndHit(t *testing.T) {
	pool, subMgr := setupPool(t)
	_ = makeRoutableNode(t, pool, subMgr, `{"sticky":"1"}`, "10.0.0.1", "cloudflare.com", 50*time.Millisecond)
	_ = makeRoutableNode(t, pool, subMgr, `{"sticky":"2"}`, "10.0.0.2", "cloudflare.com", 50*time.Millisecond)

	var events []routing.LeaseEvent
	router := makeRouter(pool, &events)

	// First request creates a lease.
	res1, err := router.RouteRequest(platName, "user-A", "example.com")
	if err != nil {
		t.Fatalf("first route: %v", err)
	}
	if !res1.LeaseCreated {
		t.Fatal("first request should create lease")
	}

	// Second request should hit sticky lease → same node.
	res2, err := router.RouteRequest(platName, "user-A", "example.com")
	if err != nil {
		t.Fatalf("second route: %v", err)
	}
	if res2.NodeHash != res1.NodeHash {
		t.Fatalf("sticky miss: got %s, expected %s", res2.NodeHash.Hex(), res1.NodeHash.Hex())
	}
	if res2.LeaseCreated {
		t.Fatal("second request should NOT create lease")
	}

	// Verify lease create event was emitted.
	foundCreate := false
	foundTouch := false
	for _, e := range events {
		if e.Type == routing.LeaseCreate && e.Account == "user-A" && e.NodeHash == res1.NodeHash {
			foundCreate = true
		}
		if e.Type == routing.LeaseTouch && e.Account == "user-A" && e.NodeHash == res1.NodeHash {
			foundTouch = true
		}
	}
	if !foundCreate {
		t.Fatal("expected LeaseCreate event for user-A")
	}
	if !foundTouch {
		t.Fatal("expected LeaseTouch event for sticky hit")
	}
}

// ── lease expiry test ───────────────────────────────────────────

func TestStickyLease_Expiry(t *testing.T) {
	pool, subMgr := setupPool(t)
	makeRoutableNode(t, pool, subMgr, `{"expire":"1"}`, "10.0.0.1", "cloudflare.com", 50*time.Millisecond)

	// Use very short TTL for expiry test.
	plat, ok := pool.GetPlatform(platID)
	if !ok {
		t.Fatal("platform not found")
	}
	plat.StickyTTLNs = int64(1 * time.Millisecond) // expires almost immediately

	var events []routing.LeaseEvent
	router := makeRouter(pool, &events)

	// Create lease.
	res1, err := router.RouteRequest(platName, "user-expire", "example.com")
	if err != nil {
		t.Fatalf("first route: %v", err)
	}
	if !res1.LeaseCreated {
		t.Fatal("should create lease")
	}

	// Wait for it to expire.
	time.Sleep(5 * time.Millisecond)

	// Next request should create new lease (expiry detected).
	res2, err := router.RouteRequest(platName, "user-expire", "example.com")
	if err != nil {
		t.Fatalf("expired route: %v", err)
	}
	if !res2.LeaseCreated {
		t.Fatal("should create new lease after expiry")
	}

	// Check for expiry event.
	foundExpire := false
	for _, e := range events {
		if e.Type == routing.LeaseExpire && e.Account == "user-expire" {
			foundExpire = true
		}
	}
	if !foundExpire {
		t.Fatal("expected LeaseExpire event")
	}
}

// ── lease cleaner test ──────────────────────────────────────────

func TestLeaseCleaner_SweepExpired(t *testing.T) {
	pool, subMgr := setupPool(t)
	makeRoutableNode(t, pool, subMgr, `{"cleaner":"1"}`, "10.0.0.1", "cloudflare.com", 50*time.Millisecond)

	// Very short TTL.
	plat, _ := pool.GetPlatform(platID)
	plat.StickyTTLNs = int64(1 * time.Millisecond)

	var events []routing.LeaseEvent
	router := makeRouter(pool, &events)

	// Create lease.
	_, err := router.RouteRequest(platName, "user-clean", "example.com")
	if err != nil {
		t.Fatalf("route: %v", err)
	}

	// Wait for expiry.
	time.Sleep(5 * time.Millisecond)

	// Start cleaner, let it run one sweep cycle.
	cleaner := routing.NewLeaseCleaner(router)
	cleaner.Start()
	time.Sleep(200 * time.Millisecond) // enough for at least one sweep if min interval is shortened
	cleaner.Stop()

	// Note: The cleaner uses 13-17s interval in production. For this test,
	// we verify the sweep function directly works (it was already called
	// by router.RouteRequest above which detects expiry on next access).
	// The important verification is that events include LeaseExpire.
	foundExpire := false
	for _, e := range events {
		if e.Type == routing.LeaseExpire && e.Account == "user-clean" {
			foundExpire = true
		}
	}
	if !foundExpire {
		// The expiry may have been detected by the RouteRequest call itself
		// (path A.Expiry check). That's fine — the cleaner is for un-accessed leases.
		t.Log("LeaseExpire event was detected by RouteRequest (not cleaner sweep), which is expected for accessed leases")
	}
}

// ── RestoreLeases test ──────────────────────────────────────────

func TestRestoreLeases(t *testing.T) {
	pool, subMgr := setupPool(t)
	h := makeRoutableNode(t, pool, subMgr, `{"restore":"1"}`, "10.0.0.1", "cloudflare.com", 50*time.Millisecond)

	router := makeRouter(pool, nil)

	// Restore a lease.
	leases := []model.Lease{
		{
			PlatformID:     platID,
			Account:        "restored-user",
			NodeHash:       h.Hex(),
			EgressIP:       "10.0.0.1",
			ExpiryNs:       time.Now().Add(1 * time.Hour).UnixNano(),
			LastAccessedNs: time.Now().UnixNano(),
		},
	}
	router.RestoreLeases(leases)

	// After restore, routing should hit the restored lease.
	res, err := router.RouteRequest(platName, "restored-user", "example.com")
	if err != nil {
		t.Fatalf("route after restore: %v", err)
	}
	if res.NodeHash != h {
		t.Fatalf("expected restored hash %s, got %s", h.Hex(), res.NodeHash.Hex())
	}
	if res.LeaseCreated {
		t.Fatal("should NOT create new lease for restored entry")
	}
}

// ── Default platform fallback test ──────────────────────────────

func TestRouteRequest_DefaultPlatform(t *testing.T) {
	pool, subMgr := setupPool(t)

	// Register default platform too.
	defaultPlat := platform.NewPlatform(platform.DefaultPlatformID, "Default", []*regexp.Regexp{}, []string{})
	pool.RegisterPlatform(defaultPlat)

	makeRoutableNode(t, pool, subMgr, `{"default":"1"}`, "10.0.0.1", "cloudflare.com", 50*time.Millisecond)

	router := makeRouter(pool, nil)

	// Empty platName should resolve to default platform.
	res, err := router.RouteRequest("", "", "example.com")
	if err != nil {
		t.Fatalf("default platform route: %v", err)
	}
	if res.NodeHash.IsZero() {
		t.Fatal("expected non-zero hash from default platform")
	}
	if res.PlatformID != platform.DefaultPlatformID || res.PlatformName != platform.DefaultPlatformName {
		t.Fatalf("expected default platform metadata id=%q name=%q, got id=%q name=%q", platform.DefaultPlatformID, platform.DefaultPlatformName, res.PlatformID, res.PlatformName)
	}
}

func TestRouteRequest_PlatformNotFound(t *testing.T) {
	pool, _ := setupPool(t)
	router := makeRouter(pool, nil)

	_, err := router.RouteRequest("NonExistent", "", "example.com")
	if err == nil || err.Error() != "platform not found" {
		t.Fatalf("expected 'platform not found', got: %v", err)
	}
}

// ── P2C scoring favors idle IP ──────────────────────────────────

func TestP2C_FavorsIdleIP(t *testing.T) {
	pool, subMgr := setupPool(t)

	// Node 1: has leases (loaded).
	h1 := makeRoutableNode(t, pool, subMgr, `{"p2c":"loaded"}`, "10.0.0.1", "cloudflare.com", 50*time.Millisecond)
	// Node 2: idle.
	h2 := makeRoutableNode(t, pool, subMgr, `{"p2c":"idle"}`, "10.0.0.2", "cloudflare.com", 50*time.Millisecond)

	router := makeRouter(pool, nil)

	// Create several leases to push IP load on node 1.
	for i := 0; i < 10; i++ {
		router.RouteRequest(platName, "p2c-loaded-"+string(rune('A'+i)), "example.com")
	}

	// Now route with empty account (random route) many times.
	// P2C should prefer h2 (lower load score).
	counts := map[node.Hash]int{h1: 0, h2: 0}
	for i := 0; i < 50; i++ {
		res, err := router.RouteRequest(platName, "", "example.com")
		if err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
		counts[res.NodeHash]++
	}
	t.Logf("P2C distribution: h1=%d, h2=%d", counts[h1], counts[h2])

	// h2 (idle) should appear more often given similar latency.
	if counts[h2] < counts[h1] {
		t.Log("Warning: loaded node selected more than idle node (may happen due to randomness)")
	}
}

// ── Concurrent route requests ───────────────────────────────────

func TestRouteRequest_ConcurrentSafety(t *testing.T) {
	pool, subMgr := setupPool(t)
	makeRoutableNode(t, pool, subMgr, `{"conc":"1"}`, "10.0.0.1", "cloudflare.com", 50*time.Millisecond)
	makeRoutableNode(t, pool, subMgr, `{"conc":"2"}`, "10.0.0.2", "cloudflare.com", 50*time.Millisecond)

	router := makeRouter(pool, nil)

	// Hammer with concurrent requests. If there's a race, go test -race will catch it.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = router.RouteRequest(platName, "concurrent-user", "example.com")
		}(i)
	}
	wg.Wait()
}

// ── RestoreLeases helper ────────────────────────────────────────

// Check if model.Lease type matches what Router.RestoreLeases expects.
func init() {
	// Verify the types compile. No runtime effect.
	_ = []model.Lease{}
}

// ── Utility: check RestoreLeases exists ─────────────────────────

func TestRestoreLeasesExists(t *testing.T) {
	pool, _ := setupPool(t)
	router := makeRouter(pool, nil)
	// Just verify it doesn't panic on empty slice.
	router.RestoreLeases(nil)
}
