package service

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
)

func newCleanupSubscriptionTestService() (*ControlPlaneService, *topology.SubscriptionManager, *topology.GlobalNodePool) {
	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
	}
	return cp, subMgr, pool
}

func TestCleanupSubscriptionCircuitOpenNodes_RemovesCircuitAndOutboundFailureNodes(t *testing.T) {
	cp, subMgr, pool := newCleanupSubscriptionTestService()

	subA := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subB := subscription.NewSubscription("sub-b", "sub-b", "https://example.com/b", true, false)
	subMgr.Register(subA)
	subMgr.Register(subB)

	circuitRaw := []byte(`{"type":"ss","server":"1.1.1.1","port":443}`)
	circuitHash := node.HashFromRawOptions(circuitRaw)
	pool.AddNodeFromSub(circuitHash, circuitRaw, subA.ID)
	subA.ManagedNodes().Store(circuitHash, []string{"circuit"})
	circuitEntry, ok := pool.GetEntry(circuitHash)
	if !ok {
		t.Fatalf("missing circuit node %s in pool", circuitHash.Hex())
	}
	circuitEntry.CircuitOpenSince.Store(time.Now().Add(-time.Minute).UnixNano())

	noOutboundErrorRaw := []byte(`{"type":"ss","server":"2.2.2.2","port":443}`)
	noOutboundErrorHash := node.HashFromRawOptions(noOutboundErrorRaw)
	pool.AddNodeFromSub(noOutboundErrorHash, noOutboundErrorRaw, subA.ID)
	subA.ManagedNodes().Store(noOutboundErrorHash, []string{"failed"})
	noOutboundErrorEntry, ok := pool.GetEntry(noOutboundErrorHash)
	if !ok {
		t.Fatalf("missing outbound failure node %s in pool", noOutboundErrorHash.Hex())
	}
	noOutboundErrorEntry.SetLastError("outbound build failed")

	healthyRaw := []byte(`{"type":"ss","server":"3.3.3.3","port":443}`)
	healthyHash := node.HashFromRawOptions(healthyRaw)
	pool.AddNodeFromSub(healthyHash, healthyRaw, subA.ID)
	subA.ManagedNodes().Store(healthyHash, []string{"healthy"})
	healthyEntry, ok := pool.GetEntry(healthyHash)
	if !ok {
		t.Fatalf("missing healthy node %s in pool", healthyHash.Hex())
	}
	outbound := testutil.NewNoopOutbound()
	healthyEntry.Outbound.Store(&outbound)

	sharedRaw := []byte(`{"type":"ss","server":"4.4.4.4","port":443}`)
	sharedHash := node.HashFromRawOptions(sharedRaw)
	pool.AddNodeFromSub(sharedHash, sharedRaw, subA.ID)
	pool.AddNodeFromSub(sharedHash, sharedRaw, subB.ID)
	subA.ManagedNodes().Store(sharedHash, []string{"shared-a"})
	subB.ManagedNodes().Store(sharedHash, []string{"shared-b"})
	sharedEntry, ok := pool.GetEntry(sharedHash)
	if !ok {
		t.Fatalf("missing shared node %s in pool", sharedHash.Hex())
	}
	sharedEntry.CircuitOpenSince.Store(time.Now().Add(-time.Minute).UnixNano())

	cleanedCount, err := cp.CleanupSubscriptionCircuitOpenNodes(subA.ID)
	if err != nil {
		t.Fatalf("CleanupSubscriptionCircuitOpenNodes: %v", err)
	}
	if cleanedCount != 3 {
		t.Fatalf("cleaned_count = %d, want %d", cleanedCount, 3)
	}

	if _, ok := subA.ManagedNodes().Load(circuitHash); ok {
		t.Fatal("circuit node should be removed from subA managed nodes")
	}
	if _, ok := subA.ManagedNodes().Load(noOutboundErrorHash); ok {
		t.Fatal("no-outbound-error node should be removed from subA managed nodes")
	}
	if _, ok := subA.ManagedNodes().Load(sharedHash); ok {
		t.Fatal("shared node should be removed from subA managed nodes")
	}
	if _, ok := subA.ManagedNodes().Load(healthyHash); !ok {
		t.Fatal("healthy node should remain in subA managed nodes")
	}

	if _, ok := pool.GetEntry(circuitHash); ok {
		t.Fatal("circuit node should be removed from pool after subA cleanup")
	}
	if _, ok := pool.GetEntry(noOutboundErrorHash); ok {
		t.Fatal("no-outbound-error node should be removed from pool after subA cleanup")
	}

	sharedEntry, ok = pool.GetEntry(sharedHash)
	if !ok {
		t.Fatal("shared node should remain in pool because subB still references it")
	}
	sharedRefs := sharedEntry.SubscriptionIDs()
	if len(sharedRefs) != 1 || sharedRefs[0] != subB.ID {
		t.Fatalf("shared node refs = %v, want [%s]", sharedRefs, subB.ID)
	}
	if _, ok := subB.ManagedNodes().Load(sharedHash); !ok {
		t.Fatal("shared node should remain in subB managed nodes")
	}

	cleanedCount, err = cp.CleanupSubscriptionCircuitOpenNodes(subA.ID)
	if err != nil {
		t.Fatalf("second CleanupSubscriptionCircuitOpenNodes: %v", err)
	}
	if cleanedCount != 0 {
		t.Fatalf("second cleaned_count = %d, want 0", cleanedCount)
	}
}

func TestCleanupSubscriptionCircuitOpenNodes_SecondConfirmSkipsRecoveredNodes(t *testing.T) {
	cp, subMgr, pool := newCleanupSubscriptionTestService()

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	recoveredRaw := []byte(`{"type":"ss","server":"5.5.5.5","port":443}`)
	recoveredHash := node.HashFromRawOptions(recoveredRaw)
	pool.AddNodeFromSub(recoveredHash, recoveredRaw, sub.ID)
	sub.ManagedNodes().Store(recoveredHash, []string{"recovering"})
	recoveredEntry, ok := pool.GetEntry(recoveredHash)
	if !ok {
		t.Fatalf("missing recovering node %s in pool", recoveredHash.Hex())
	}
	recoveredEntry.CircuitOpenSince.Store(time.Now().Add(-time.Minute).UnixNano())

	failedRaw := []byte(`{"type":"ss","server":"6.6.6.6","port":443}`)
	failedHash := node.HashFromRawOptions(failedRaw)
	pool.AddNodeFromSub(failedHash, failedRaw, sub.ID)
	sub.ManagedNodes().Store(failedHash, []string{"failed"})
	failedEntry, ok := pool.GetEntry(failedHash)
	if !ok {
		t.Fatalf("missing failed node %s in pool", failedHash.Hex())
	}
	failedEntry.SetLastError("outbound build failed")

	hookCalled := false
	cleanedCount, err := cp.cleanupSubscriptionCircuitOpenNodesWithHook(sub.ID, func() {
		hookCalled = true
		// Simulate node recovery in TOCTOU window.
		recoveredEntry.CircuitOpenSince.Store(0)
	})
	if err != nil {
		t.Fatalf("cleanupSubscriptionCircuitOpenNodesWithHook: %v", err)
	}
	if !hookCalled {
		t.Fatal("betweenScans hook was not called")
	}
	if cleanedCount != 1 {
		t.Fatalf("cleaned_count = %d, want 1", cleanedCount)
	}

	if _, ok := sub.ManagedNodes().Load(recoveredHash); !ok {
		t.Fatal("recovered node should remain in managed nodes after second confirmation")
	}
	if _, ok := pool.GetEntry(recoveredHash); !ok {
		t.Fatal("recovered node should remain in pool after second confirmation")
	}
	if _, ok := sub.ManagedNodes().Load(failedHash); ok {
		t.Fatal("failed node should be removed from managed nodes")
	}
	if _, ok := pool.GetEntry(failedHash); ok {
		t.Fatal("failed node should be removed from pool")
	}
}

func TestCleanupSubscriptionCircuitOpenNodes_SubscriptionNotFound(t *testing.T) {
	cp, _, _ := newCleanupSubscriptionTestService()

	_, err := cp.CleanupSubscriptionCircuitOpenNodes("missing-sub")
	if err == nil {
		t.Fatal("expected not found error")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("error type = %T, want *ServiceError", err)
	}
	if svcErr.Code != "NOT_FOUND" {
		t.Fatalf("error code = %q, want NOT_FOUND", svcErr.Code)
	}
}
