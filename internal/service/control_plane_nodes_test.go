package service

import (
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/geoip"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/probe"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
)

func newNodeListTestPool(subMgr *topology.SubscriptionManager) *topology.GlobalNodePool {
	return topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
}

func addRoutableNodeForSubscription(
	t *testing.T,
	pool *topology.GlobalNodePool,
	sub *subscription.Subscription,
	raw []byte,
	egressIP string,
) node.Hash {
	return addRoutableNodeForSubscriptionWithTag(t, pool, sub, raw, egressIP, "tag")
}

func addRoutableNodeForSubscriptionWithTag(
	t *testing.T,
	pool *topology.GlobalNodePool,
	sub *subscription.Subscription,
	raw []byte,
	egressIP string,
	tag string,
) node.Hash {
	t.Helper()

	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, sub.ID)
	sub.ManagedNodes().Store(hash, []string{tag})

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s not found after add", hash.Hex())
	}
	entry.SetEgressIP(netip.MustParseAddr(egressIP))
	if entry.LatencyTable == nil {
		t.Fatalf("node %s latency table not initialized", hash.Hex())
	}
	entry.LatencyTable.Update("example.com", 25*time.Millisecond, 10*time.Minute)
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	pool.NotifyNodeDirty(hash)
	return hash
}

func TestListNodes_PlatformAndSubscriptionFiltersReturnIntersection(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	plat := platform.NewPlatform("plat-1", "plat", nil, nil)
	pool.RegisterPlatform(plat)

	subA := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subB := subscription.NewSubscription("sub-b", "sub-b", "https://example.com/b", true, false)
	subMgr.Register(subA)
	subMgr.Register(subB)

	hashA := addRoutableNodeForSubscription(
		t,
		pool,
		subA,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.10",
	)
	_ = addRoutableNodeForSubscription(
		t,
		pool,
		subB,
		[]byte(`{"type":"ss","server":"2.2.2.2","port":443}`),
		"203.0.113.11",
	)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}
	filters := NodeFilters{
		PlatformID:     &plat.ID,
		SubscriptionID: &subA.ID,
	}

	nodes, err := cp.ListNodes(filters)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("intersection size = %d, want 1", len(nodes))
	}
	if nodes[0].NodeHash != hashA.Hex() {
		t.Fatalf("intersection node hash = %q, want %q", nodes[0].NodeHash, hashA.Hex())
	}
}

func TestListNodes_SubscriptionFilterSkipsStaleManagedNodes(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	staleHash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"9.9.9.9","port":443}`))
	sub.ManagedNodes().Store(staleHash, []string{"stale"})

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}
	filters := NodeFilters{
		SubscriptionID: &sub.ID,
	}

	nodes, err := cp.ListNodes(filters)
	if err != nil {
		t.Fatalf("ListNodes with stale hash: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("nodes with stale managed hash = %d, want 0", len(nodes))
	}

	liveHash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.20",
	)

	nodes, err = cp.ListNodes(filters)
	if err != nil {
		t.Fatalf("ListNodes with stale+live hashes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes with stale+live hashes = %d, want 1", len(nodes))
	}
	if nodes[0].NodeHash != liveHash.Hex() {
		t.Fatalf("live node hash = %q, want %q", nodes[0].NodeHash, liveHash.Hex())
	}
}

func TestGetNode_TagIncludesSubscriptionNamePrefix(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.30",
	)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	got, err := cp.GetNode(hash.Hex())
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if len(got.Tags) != 1 {
		t.Fatalf("tags len = %d, want 1", len(got.Tags))
	}
	if got.Tags[0].Tag != "sub-a/tag" {
		t.Fatalf("tag = %q, want %q", got.Tags[0].Tag, "sub-a/tag")
	}
}

func TestGetNode_ReferenceLatencyMsUsesAuthorityAverage(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.30",
	)

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s missing", hash.Hex())
	}
	entry.LatencyTable.LoadEntry("cloudflare.com", node.DomainLatencyStats{
		Ewma:        40 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	entry.LatencyTable.LoadEntry("github.com", node.DomainLatencyStats{
		Ewma:        60 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        5 * time.Millisecond,
		LastUpdated: time.Now(),
	})

	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	cfg := config.NewDefaultRuntimeConfig()
	cfg.LatencyAuthorities = []string{"cloudflare.com", "github.com", "google.com"}
	runtimeCfg.Store(cfg)

	cp := &ControlPlaneService{
		Pool:       pool,
		SubMgr:     subMgr,
		GeoIP:      &geoip.Service{},
		RuntimeCfg: runtimeCfg,
	}

	got, err := cp.GetNode(hash.Hex())
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.ReferenceLatencyMs == nil {
		t.Fatal("reference_latency_ms should be present")
	}
	if *got.ReferenceLatencyMs != 50 {
		t.Fatalf("reference_latency_ms = %v, want 50", *got.ReferenceLatencyMs)
	}
}

func TestListNodes_ProbedSinceUsesLastLatencyProbeAttempt(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.30",
	)

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s missing", hash.Hex())
	}

	latencyAttempt := time.Now().Add(-2 * time.Minute).UnixNano()
	entry.LastLatencyProbeAttempt.Store(latencyAttempt)
	// Keep egress update older to ensure filter is using LastLatencyProbeAttempt.
	entry.LastEgressUpdate.Store(time.Now().Add(-10 * time.Minute).UnixNano())

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	before := time.Unix(0, latencyAttempt).Add(-1 * time.Minute)
	nodes, err := cp.ListNodes(NodeFilters{ProbedSince: &before})
	if err != nil {
		t.Fatalf("ListNodes(before): %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("ListNodes(before) len = %d, want 1", len(nodes))
	}

	after := time.Unix(0, latencyAttempt).Add(1 * time.Minute)
	nodes, err = cp.ListNodes(NodeFilters{ProbedSince: &after})
	if err != nil {
		t.Fatalf("ListNodes(after): %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("ListNodes(after) len = %d, want 0", len(nodes))
	}
}

func TestListNodes_TagKeywordFuzzyMatchIsCaseInsensitive(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	matchHash := addRoutableNodeForSubscriptionWithTag(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.30",
		"hongkong-fast-01",
	)
	_ = addRoutableNodeForSubscriptionWithTag(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"2.2.2.2","port":443}`),
		"203.0.113.31",
		"japan-slow-01",
	)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	keyword := "FAST"
	nodes, err := cp.ListNodes(NodeFilters{TagKeyword: &keyword})
	if err != nil {
		t.Fatalf("ListNodes(tag_keyword): %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("ListNodes(tag_keyword) len = %d, want 1", len(nodes))
	}
	if nodes[0].NodeHash != matchHash.Hex() {
		t.Fatalf("ListNodes(tag_keyword) hash = %q, want %q", nodes[0].NodeHash, matchHash.Hex())
	}
}

func TestListNodes_RegionFilterAndSummaryPreferStoredRegion(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.40",
	)

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s missing", hash.Hex())
	}
	entry.SetEgressRegion("jp")

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{}, // empty service returns "", forcing stored-region path
	}

	region := "jp"
	nodes, err := cp.ListNodes(NodeFilters{Region: &region})
	if err != nil {
		t.Fatalf("ListNodes(region): %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeHash != hash.Hex() {
		t.Fatalf("region-filtered nodes = %+v, want [%s]", nodes, hash.Hex())
	}

	got, err := cp.GetNode(hash.Hex())
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Region != "jp" {
		t.Fatalf("summary region: got %q, want %q", got.Region, "jp")
	}
}

func TestProbeEgress_ReturnsRegion(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.60",
	)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{}, // empty service keeps focus on stored region from loc
		ProbeMgr: probe.NewProbeManager(probe.ProbeConfig{
			Pool: pool,
			Fetcher: func(_ node.Hash, _ string) ([]byte, time.Duration, error) {
				return []byte("ip=198.51.100.88\nloc=JP"), 20 * time.Millisecond, nil
			},
		}),
	}

	got, err := cp.ProbeEgress(hash.Hex())
	if err != nil {
		t.Fatalf("ProbeEgress: %v", err)
	}
	if got.EgressIP != "198.51.100.88" {
		t.Fatalf("egress_ip: got %q, want %q", got.EgressIP, "198.51.100.88")
	}
	if got.Region != "jp" {
		t.Fatalf("region: got %q, want %q", got.Region, "jp")
	}
}
