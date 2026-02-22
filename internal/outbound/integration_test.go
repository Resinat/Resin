package outbound_test

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
)

// TestEndToEnd_NodeEnterRoutableView verifies the full lifecycle:
// node added to pool → EnsureNodeOutbound → outbound set →
// latency recorded → egress IP set → platform filter passes → node in routable view.
func TestEndToEnd_NodeEnterRoutableView(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()

	// Create a subscription with the node.
	rawOpts := json.RawMessage(`{"type":"e2e-test"}`)
	hash := node.HashFromRawOptions(rawOpts)

	// Use a no-op geo lookup (region filters empty → passes).
	geoLookup := func(_ netip.Addr) string { return "US" }

	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              geoLookup,
		MaxLatencyTableEntries: 10,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	// Register a platform with no regex/region filters.
	platCfg := platform.NewPlatform("test-plat-id", "test-plat", []*regexp.Regexp{}, []string{})
	pool.RegisterPlatform(platCfg)

	// Register subscription and set its managed nodes.
	sub := subscription.NewSubscription("sub-1", "Test Sub", "https://example.com/sub", true, false)
	subMgr.Register(sub)
	managedNodes := xsync.NewMap[node.Hash, []string]()
	managedNodes.Store(hash, []string{"tag1"})
	sub.SwapManagedNodes(managedNodes)

	pool.AddNodeFromSub(hash, rawOpts, "sub-1")

	// At this point, node is in pool but no outbound, no latency, no egress IP.
	// Platform should NOT include it.
	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("node not found in pool after AddNodeFromSub")
	}
	if entry.HasOutbound() {
		t.Fatal("expected no outbound before EnsureNodeOutbound")
	}

	// Check platform does NOT contain the node yet.
	plat, ok := pool.GetPlatform("test-plat-id")
	if !ok {
		t.Fatal("platform not found")
	}
	if plat.View().Contains(hash) {
		t.Fatal("node should NOT be in routable view yet (no outbound/latency/egress)")
	}

	// Step 1: Create outbound.
	obMgr := outbound.NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	obMgr.EnsureNodeOutbound(hash)
	if !entry.HasOutbound() {
		t.Fatal("expected HasOutbound() == true after EnsureNodeOutbound")
	}

	// Step 2: Record latency (simulate a successful probe).
	entry.LatencyTable.Update("cloudflare.com", 50*time.Millisecond, 10*time.Minute)

	// Step 3: Set egress IP.
	ip := netip.MustParseAddr("203.0.113.1")
	entry.SetEgressIP(ip)

	// Step 4: Trigger platform re-evaluation.
	pool.NotifyNodeDirty(hash)

	// Now platform should include the node.
	if !plat.View().Contains(hash) {
		t.Fatal("node should be in routable view after outbound + latency + egress IP set")
	}

	// Step 5: Remove outbound.
	obMgr.RemoveNodeOutbound(entry)
	pool.NotifyNodeDirty(hash)

	// After removing outbound, platform should exclude the node.
	if plat.View().Contains(hash) {
		t.Fatal("node should NOT be in routable view after outbound removed")
	}
}
