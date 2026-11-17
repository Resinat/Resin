package topology

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
)

func addChainMember(pool *GlobalNodePool, sub *subscription.Subscription, raw string) node.Hash {
	hash := node.HashFromRawOptions([]byte(raw))
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"node"}})
	pool.AddNodeFromSub(hash, []byte(raw), sub.ID)
	return hash
}

func TestNodeChainRoleAggregatesAndFallsBack(t *testing.T) {
	pool, subs := newHealthTestPool(3)
	ordinary := subs.Lookup("s1")
	chain := subscription.NewSubscription("chain", "chain", "", true, false)
	chain.SetChainConfig(true, false)
	relay := subscription.NewSubscription("relay", "relay", "", false, false)
	relay.SetChainConfig(false, true)
	subs.Register(chain)
	subs.Register(relay)
	raw := `{"type":"http","server":"shared","server_port":80}`
	hash := addChainMember(pool, ordinary, raw)
	e, _ := pool.GetEntry(hash)
	ob := testutil.NewNoopOutbound()
	e.Outbound.Store(&ob)
	pool.RecordResult(hash, true)
	ip := netip.MustParseAddr("203.0.113.5")
	loc := "us"
	pool.UpdateNodeEgressIP(hash, &ip, &loc)
	latency := time.Millisecond
	pool.RecordLatency(hash, "speed.example", &latency)
	oldVersion := e.RoutingVersion()
	addChainMember(pool, chain, raw)
	if e.RoutingRole() != node.RoleChain || e.RoutingVersion() == oldVersion {
		t.Fatal("chain role was not published")
	}
	if e.GetEgressIP().IsValid() || e.HasLatency() || !e.IsCircuitOpen() {
		t.Fatal("old mode measurements remained routable")
	}
	if pool.ApplyFeedback(hash, oldVersion, node.Feedback{Result: boolPtr(true)}) {
		t.Fatal("old mode feedback was accepted")
	}
	addChainMember(pool, relay, raw)
	if e.RoutingRole() != node.RoleRelay {
		t.Fatal("relay must win, including a disabled holder")
	}
	pool.RemoveNodeFromSub(hash, relay.ID)
	if e.RoutingRole() != node.RoleChain {
		t.Fatal("removing relay holder must reveal chain role")
	}
	pool.RemoveNodeFromSub(hash, chain.ID)
	if e.RoutingRole() != node.RoleOrdinary {
		t.Fatal("removing chain holder must restore ordinary mode")
	}
	if string(e.RawOptions) != raw || pool.Size() != 1 {
		t.Fatal("roles must preserve the shared node identity")
	}
	version := e.RoutingVersion()
	pool.RemoveNodeFromSub(hash, ordinary.ID)
	addChainMember(pool, ordinary, raw)
	if pool.ApplyFeedback(hash, version, node.Feedback{Result: boolPtr(true)}) {
		t.Fatal("feedback reached a re-added node")
	}
}

func TestRelayViewTracksHealthRegionAndEnabledState(t *testing.T) {
	pool, subs := newHealthTestPool(2)
	sub := subs.Lookup("s1")
	sub.SetChainConfig(false, true)
	hash := addChainMember(pool, sub, `{"type":"http","server":"relay"}`)
	e, _ := pool.GetEntry(hash)
	ob := testutil.NewNoopOutbound()
	e.Outbound.Store(&ob)
	pool.RecordResult(hash, true)
	var availabilityEvents int
	pool.SetOnRelayAvailable(func() { availabilityEvents++ })
	region := "us"
	pool.UpdateNodeEgressIP(hash, nil, &region)
	if !pool.RelayView("us", "tcp").Contains(hash) {
		t.Fatal("healthy relay missing from regional view")
	}
	region = "jp"
	pool.UpdateNodeEgressIP(hash, nil, &region)
	if pool.RelayView("us", "tcp").Contains(hash) || !pool.RelayView("jp", "tcp").Contains(hash) {
		t.Fatal("region change did not update membership")
	}
	if availabilityEvents != 0 {
		t.Fatal("region changes must not signal a global recovery")
	}
	pool.RecordResult(hash, false)
	pool.RecordResult(hash, false)
	if pool.RelayView("", "tcp").Contains(hash) {
		t.Fatal("circuit-open relay remains selectable")
	}
	pool.RecordResult(hash, true)
	if availabilityEvents != 1 {
		t.Fatal("first recovered relay must signal availability")
	}
	sub.SetEnabled(false)
	pool.RebuildAllPlatforms()
	if pool.RelayView("", "tcp").Contains(hash) {
		t.Fatal("disabled relay remains selectable")
	}
	sub.SetEnabled(true)
	pool.RebuildAllPlatforms()
	if !pool.RelayView("", "tcp").Contains(hash) {
		t.Fatal("re-enabled relay did not return")
	}
	pool.RemoveNodeFromSub(hash, sub.ID)
	if pool.RelayView("", "tcp").Contains(hash) {
		t.Fatal("deleted relay remains selectable")
	}
}

func boolPtr(v bool) *bool { return &v }

func TestModeSwitchDoesNotReuseOldProbeErrorForImmediateEviction(t *testing.T) {
	pool, subs := newHealthTestPool(3)
	sub := subs.Lookup("s1")
	sub.SetEphemeral(true)
	sub.SetEphemeralNodeEvictDelayNs(int64(72 * time.Hour))
	hash := addChainMember(pool, sub, `{"type":"http","server":"landing"}`)
	entry, _ := pool.GetEntry(hash)
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	pool.RecordResult(hash, true)
	failed := false
	message := "previous latency probe timeout"
	pool.ApplyFeedback(hash, entry.RoutingVersion(), node.Feedback{Result: &failed, LastError: &message})
	if !entry.IsHealthy() {
		t.Fatal("one failed probe should not trip the circuit")
	}
	sub.SetChainConfig(true, false)
	pool.ReconcileNodeRole(hash)
	cleaner := NewEphemeralCleaner(subs, pool)
	if cleaner.shouldEvictEntry(entry, time.Now().UnixNano(), sub.EphemeralNodeEvictDelayNs()) {
		t.Fatal("old probe error bypassed 72-hour eviction delay during mode rebuild")
	}
	if entry.GetLastError() != "" {
		t.Fatal("old mode error survived mode switch")
	}
	// Preserve the existing fast-eviction policy for an actual new build error.
	message = "outbound build: invalid options"
	pool.ApplyFeedback(hash, entry.RoutingVersion(), node.Feedback{LastError: &message})
	if !cleaner.shouldEvictEntry(entry, time.Now().UnixNano(), sub.EphemeralNodeEvictDelayNs()) {
		t.Fatal("actual build failure no longer permits fast eviction")
	}
}
