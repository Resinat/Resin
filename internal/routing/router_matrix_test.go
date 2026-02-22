package routing

import (
	"encoding/json"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
)

type getEntryCountPool struct {
	inner    PoolAccessor
	getCalls int
}

func (p *getEntryCountPool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	p.getCalls++
	return p.inner.GetEntry(hash)
}

func (p *getEntryCountPool) GetPlatform(id string) (*platform.Platform, bool) {
	return p.inner.GetPlatform(id)
}

func (p *getEntryCountPool) GetPlatformByName(name string) (*platform.Platform, bool) {
	return p.inner.GetPlatformByName(name)
}

func (p *getEntryCountPool) RangePlatforms(fn func(*platform.Platform) bool) {
	p.inner.RangePlatforms(fn)
}

type transientMissingPool struct {
	inner  PoolAccessor
	target node.Hash
	missed atomic.Bool
}

func (p *transientMissingPool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	if hash == p.target && !p.missed.Swap(true) {
		return nil, false
	}
	return p.inner.GetEntry(hash)
}

func (p *transientMissingPool) GetPlatform(id string) (*platform.Platform, bool) {
	return p.inner.GetPlatform(id)
}

func (p *transientMissingPool) GetPlatformByName(name string) (*platform.Platform, bool) {
	return p.inner.GetPlatformByName(name)
}

func (p *transientMissingPool) RangePlatforms(fn func(*platform.Platform) bool) {
	p.inner.RangePlatforms(fn)
}

func TestRouteRequest_DefaultPlatformRequiresWellKnownID(t *testing.T) {
	pool := newRouterTestPool()
	// Default name alone is not sufficient; only the well-known ID is treated as default.
	plat := platform.NewPlatform("legacy-default-id", platform.DefaultPlatformName, nil, nil)
	pool.addPlatform(plat)

	defaultHash, defaultEntry := newRoutableEntry(t, `{"id":"default-by-name"}`, "198.51.100.10")
	pool.addEntry(defaultHash, defaultEntry)
	pool.rebuildPlatformView(plat)

	router := newTestRouter(pool, nil)
	_, err := router.RouteRequest("", "", "https://example.com")
	if !errors.Is(err, ErrPlatformNotFound) {
		t.Fatalf("expected ErrPlatformNotFound without default ID, got %v", err)
	}
}

func TestRouteRequest_EmptyAccountStaleViewReturnsError(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-stale", "Plat-Stale", nil, nil)
	pool.addPlatform(plat)

	h, e := newRoutableEntry(t, `{"id":"stale-view-node"}`, "203.0.113.10")
	pool.addEntry(h, e)
	pool.rebuildPlatformView(plat)
	if !plat.View().Contains(h) {
		t.Fatal("setup failed: expected node in view")
	}

	// Create stale view: remove from pool without notifying platform.
	pool.removeEntry(h)

	router := newTestRouter(pool, nil)
	_, err := router.RouteRequest(plat.Name, "", "https://example.com")
	if !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("expected ErrNoAvailableNodes for stale view, got %v", err)
	}
}

func TestRouteRequest_EmptyAccount_TransientMissingEntryRetriesOnce(t *testing.T) {
	basePool := newRouterTestPool()
	plat := platform.NewPlatform("plat-retry", "Plat-Retry", nil, nil)
	basePool.addPlatform(plat)

	h, e := newRoutableEntry(t, `{"id":"transient-miss"}`, "203.0.113.55")
	basePool.addEntry(h, e)
	basePool.rebuildPlatformView(plat)
	if !plat.View().Contains(h) {
		t.Fatal("setup failed: expected node in view")
	}

	pool := &transientMissingPool{
		inner:  basePool,
		target: h,
	}
	router := newTestRouter(pool, nil)

	res, err := router.RouteRequest(plat.Name, "", "https://example.com")
	if err != nil {
		t.Fatalf("expected retry to recover from transient miss, got %v", err)
	}
	if res.NodeHash != h {
		t.Fatalf("unexpected selected node after retry: got=%s want=%s", res.NodeHash.Hex(), h.Hex())
	}
}

func TestRandomRoute_SingleNodeTrustsViewWithoutPoolValidation(t *testing.T) {
	basePool := newRouterTestPool()
	plat := platform.NewPlatform("plat-single", "Plat-Single", nil, nil)
	basePool.addPlatform(plat)

	h, e := newRoutableEntry(t, `{"id":"single-view-only"}`, "203.0.113.42")
	basePool.addEntry(h, e)
	basePool.rebuildPlatformView(plat)

	countingPool := &getEntryCountPool{inner: basePool}
	stats := NewIPLoadStats()

	got, err := randomRoute(
		plat,
		stats,
		countingPool,
		"example.com",
		[]string{"cloudflare.com"},
		10*time.Minute,
	)
	if err != nil {
		t.Fatalf("randomRoute failed: %v", err)
	}
	if got != h {
		t.Fatalf("unexpected selected node: got=%s want=%s", got.Hex(), h.Hex())
	}
	if countingPool.getCalls != 0 {
		t.Fatalf("single-node randomRoute should trust view and skip pool validation, getEntry calls=%d", countingPool.getCalls)
	}
}

func TestCompareLatencies_ComparableTargetDomain(t *testing.T) {
	pool := newRouterTestPool()

	h1, e1 := newRoutableEntry(t, `{"id":"cmp-a"}`, "203.0.113.20")
	h2, e2 := newRoutableEntry(t, `{"id":"cmp-b"}`, "203.0.113.21")
	e1.LatencyTable.Update("example.com", 25*time.Millisecond, 10*time.Minute)
	e2.LatencyTable.Update("example.com", 55*time.Millisecond, 10*time.Minute)
	waitForDomainLatency(t, e1, "example.com")
	waitForDomainLatency(t, e2, "example.com")

	pool.addEntry(h1, e1)
	pool.addEntry(h2, e2)

	lat1, lat2 := compareLatencies(
		h1, h2, pool,
		"example.com",
		[]string{"cloudflare.com"},
		10*time.Minute,
	)
	if lat1 != 25*time.Millisecond || lat2 != 55*time.Millisecond {
		t.Fatalf("unexpected comparable target-domain latencies: got=(%v,%v)", lat1, lat2)
	}
}

func TestCompareLatencies_IncomparableFallsBackToZero(t *testing.T) {
	pool := newRouterTestPool()

	h1, e1 := newRoutableEntry(t, `{"id":"incmp-a"}`, "203.0.113.30")
	h2, e2 := newRoutableEntry(t, `{"id":"incmp-b"}`, "203.0.113.31")
	e1.LatencyTable.Update("example.com", 20*time.Millisecond, 10*time.Minute)
	waitForDomainLatency(t, e1, "example.com")
	// h2 intentionally has no example.com sample.

	pool.addEntry(h1, e1)
	pool.addEntry(h2, e2)

	lat1, lat2 := compareLatencies(
		h1, h2, pool,
		"example.com",
		nil, // no authority fallback for this matrix case
		10*time.Minute,
	)
	if lat1 != 0 || lat2 != 0 {
		t.Fatalf("expected incomparable case to fall back to zero latency, got=(%v,%v)", lat1, lat2)
	}
}

func TestChooseSameIPRotationCandidate_PicksLowestLatency(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-rotate", "Plat-Rotate", nil, nil)
	pool.addPlatform(plat)

	currentHash, currentEntry := newRoutableEntry(t, `{"id":"current-rotate"}`, "198.51.100.77")
	candidateA, entryA := newRoutableEntry(t, `{"id":"candidate-a-rotate"}`, "198.51.100.77")
	candidateB, entryB := newRoutableEntry(t, `{"id":"candidate-b-rotate"}`, "198.51.100.77")
	entryA.LatencyTable.Update("example.com", 70*time.Millisecond, 10*time.Minute)
	entryB.LatencyTable.Update("example.com", 15*time.Millisecond, 10*time.Minute)
	waitForDomainLatency(t, entryA, "example.com")
	waitForDomainLatency(t, entryB, "example.com")

	pool.addEntry(currentHash, currentEntry)
	pool.addEntry(candidateA, entryA)
	pool.addEntry(candidateB, entryB)
	pool.rebuildPlatformView(plat)

	// Invalidate current lease node and keep same-IP candidates routable.
	currentEntry.CircuitOpenSince.Store(time.Now().UnixNano())
	plat.NotifyDirty(
		currentHash,
		pool.GetEntry,
		func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
		func(_ netip.Addr) string { return "" },
	)

	hash, ok := chooseSameIPRotationCandidate(
		plat,
		pool,
		netip.MustParseAddr("198.51.100.77"),
		"example.com",
		[]string{"cloudflare.com"},
		10*time.Minute,
	)
	if !ok {
		t.Fatal("expected same-ip rotation candidate")
	}
	if hash != candidateB {
		t.Fatalf("expected lowest-latency candidate %s, got %s", candidateB.Hex(), hash.Hex())
	}
}

func TestRouteRequest_SameIPRotationMissRecreatesLease(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-miss", "Plat-Miss", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	currentHash, currentEntry := newRoutableEntry(t, `{"id":"current-miss"}`, "203.0.113.60")
	replacementHash, replacementEntry := newRoutableEntry(t, `{"id":"replacement-miss"}`, "203.0.113.61")
	pool.addEntry(currentHash, currentEntry)
	pool.addEntry(replacementHash, replacementEntry)
	pool.rebuildPlatformView(plat)

	// Invalidate current lease node so route must rotate.
	currentEntry.CircuitOpenSince.Store(time.Now().UnixNano())
	plat.NotifyDirty(
		currentHash,
		pool.GetEntry,
		func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
		func(_ netip.Addr) string { return "" },
	)

	var events []LeaseEvent
	router := newTestRouter(pool, func(e LeaseEvent) {
		events = append(events, e)
	})
	state, _ := router.states.LoadOrCompute(plat.ID, func() (*PlatformRoutingState, bool) {
		return NewPlatformRoutingState(), false
	})

	oldExpiry := time.Now().Add(time.Hour).UnixNano()
	oldLease := Lease{
		NodeHash:       currentHash,
		EgressIP:       currentEntry.GetEgressIP(),
		ExpiryNs:       oldExpiry,
		LastAccessedNs: time.Now().UnixNano(),
	}
	state.Leases.CreateLease("acct-miss", oldLease)

	res, err := router.RouteRequest(plat.Name, "acct-miss", "https://example.com")
	if err != nil {
		t.Fatalf("route failed: %v", err)
	}
	if !res.LeaseCreated {
		t.Fatal("expected lease recreation when same-ip rotation misses")
	}
	if res.NodeHash != replacementHash {
		t.Fatalf("expected replacement node %s, got %s", replacementHash.Hex(), res.NodeHash.Hex())
	}

	newLease, ok := state.Leases.GetLease("acct-miss")
	if !ok {
		t.Fatal("expected recreated lease")
	}
	if newLease.NodeHash != replacementHash {
		t.Fatalf("lease node not updated: got=%s want=%s", newLease.NodeHash.Hex(), replacementHash.Hex())
	}
	if newLease.ExpiryNs == oldExpiry {
		t.Fatalf("recreated lease must have new expiry, still got old %d", oldExpiry)
	}
	if got := state.IPLoadStats.Get(oldLease.EgressIP); got != 0 {
		t.Fatalf("old egress ip load should be 0, got %d", got)
	}
	if got := state.IPLoadStats.Get(replacementEntry.GetEgressIP()); got != 1 {
		t.Fatalf("new egress ip load should be 1, got %d", got)
	}

	foundRemove := false
	foundCreate := false
	for _, e := range events {
		if e.Type == LeaseRemove && e.Account == "acct-miss" && e.NodeHash == currentHash {
			foundRemove = true
		}
		if e.Type == LeaseCreate && e.Account == "acct-miss" && e.NodeHash == replacementHash {
			foundCreate = true
		}
	}
	if !foundRemove {
		t.Fatal("expected LeaseRemove event for invalidated old lease")
	}
	if !foundCreate {
		t.Fatal("expected LeaseCreate event for recreated lease")
	}
}

func TestLeaseCleaner_SweepExpiresLeaseDeterministically(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-cleaner", "Plat-Cleaner", nil, nil)
	pool.addPlatform(plat)

	var events []LeaseEvent
	router := newTestRouter(pool, func(e LeaseEvent) {
		events = append(events, e)
	})
	state, _ := router.states.LoadOrCompute(plat.ID, func() (*PlatformRoutingState, bool) {
		return NewPlatformRoutingState(), false
	})

	h := node.HashFromRawOptions(json.RawMessage(`{"id":"expired-lease-node"}`))
	ip := netip.MustParseAddr("203.0.113.88")
	state.Leases.CreateLease("acct-expire", Lease{
		NodeHash:       h,
		EgressIP:       ip,
		ExpiryNs:       time.Now().Add(-1 * time.Minute).UnixNano(),
		LastAccessedNs: time.Now().Add(-2 * time.Minute).UnixNano(),
	})
	if got := state.IPLoadStats.Get(ip); got != 1 {
		t.Fatalf("setup ip load: got=%d want=1", got)
	}

	cleaner := NewLeaseCleaner(router)
	cleaner.sweep()

	if _, ok := state.Leases.GetLease("acct-expire"); ok {
		t.Fatal("expected expired lease to be removed by sweep")
	}
	if got := state.IPLoadStats.Get(ip); got != 0 {
		t.Fatalf("expected ip load to be decremented after sweep, got %d", got)
	}

	foundExpire := false
	for _, e := range events {
		if e.Type == LeaseExpire && e.PlatformID == plat.ID && e.Account == "acct-expire" && e.NodeHash == h {
			foundExpire = true
			break
		}
	}
	if !foundExpire {
		t.Fatal("expected LeaseExpire event from sweep")
	}
}
