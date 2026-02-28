package service

import (
	"encoding/json"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
)

func newLeaseInheritanceTestService() (*ControlPlaneService, *platform.Platform) {
	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	plat := platform.NewPlatform("plat-1", "Default", nil, nil)
	pool.RegisterPlatform(plat)

	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"cloudflare.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
	})

	return &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		Router: router,
	}, plat
}

func seedLease(t *testing.T, cp *ControlPlaneService, lease model.Lease) {
	t.Helper()
	if err := cp.Router.UpsertLease(lease); err != nil {
		t.Fatalf("UpsertLease: %v", err)
	}
}

func assertServiceErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("error type: got %T, want *ServiceError", err)
	}
	if svcErr.Code != code {
		t.Fatalf("error code: got %q, want %q", svcErr.Code, code)
	}
}

func seedSharedNodeAcrossSubscriptions(
	t *testing.T,
	cp *ControlPlaneService,
	hash node.Hash,
	olderSubID string,
	olderSubName string,
	olderCreatedAtNs int64,
	olderTags []string,
	newerSubID string,
	newerSubName string,
	newerCreatedAtNs int64,
	newerTags []string,
) {
	t.Helper()

	older := subscription.NewSubscription(olderSubID, olderSubName, "https://example.com/"+olderSubID, true, false)
	older.CreatedAtNs = olderCreatedAtNs
	olderManaged := subscription.NewManagedNodes()
	olderManaged.StoreNode(hash, subscription.ManagedNode{Tags: olderTags})
	older.SwapManagedNodes(olderManaged)

	newer := subscription.NewSubscription(newerSubID, newerSubName, "https://example.com/"+newerSubID, true, false)
	newer.CreatedAtNs = newerCreatedAtNs
	newerManaged := subscription.NewManagedNodes()
	newerManaged.StoreNode(hash, subscription.ManagedNode{Tags: newerTags})
	newer.SwapManagedNodes(newerManaged)

	cp.SubMgr.Register(older)
	cp.SubMgr.Register(newer)

	raw := json.RawMessage(`{"type":"ss","server":"198.51.100.10","port":443}`)
	cp.Pool.AddNodeFromSub(hash, raw, older.ID)
	cp.Pool.AddNodeFromSub(hash, raw, newer.ID)
}

func TestGetLease_NodeTagUsesEarliestSubscriptionThenMinTag(t *testing.T) {
	cp, plat := newLeaseInheritanceTestService()

	hash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"198.51.100.10","port":443}`))
	seedSharedNodeAcrossSubscriptions(
		t,
		cp,
		hash,
		"sub-old",
		"Z-Provider",
		100,
		[]string{"zz", "aa"},
		"sub-new",
		"A-Provider",
		200,
		[]string{"00"},
	)

	now := time.Now().UnixNano()
	seedLease(t, cp, model.Lease{
		PlatformID:     plat.ID,
		Account:        "alice",
		NodeHash:       hash.Hex(),
		EgressIP:       "203.0.113.10",
		CreatedAtNs:    now - int64(time.Minute),
		ExpiryNs:       now + int64(time.Minute),
		LastAccessedNs: now,
	})

	got, err := cp.GetLease(plat.ID, "alice")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if got.NodeTag != "Z-Provider/aa" {
		t.Fatalf("node_tag: got %q, want %q", got.NodeTag, "Z-Provider/aa")
	}
}

func TestListLeases_NodeTagUsesEarliestSubscriptionThenMinTag(t *testing.T) {
	cp, plat := newLeaseInheritanceTestService()

	hash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"203.0.113.20","port":443}`))
	seedSharedNodeAcrossSubscriptions(
		t,
		cp,
		hash,
		"sub-old-list",
		"OldSub",
		50,
		[]string{"b", "a"},
		"sub-new-list",
		"NewSub",
		60,
		[]string{"0"},
	)

	now := time.Now().UnixNano()
	seedLease(t, cp, model.Lease{
		PlatformID:     plat.ID,
		Account:        "bob",
		NodeHash:       hash.Hex(),
		EgressIP:       "198.51.100.3",
		CreatedAtNs:    now - int64(time.Minute),
		ExpiryNs:       now + int64(time.Minute),
		LastAccessedNs: now,
	})

	leases, err := cp.ListLeases(plat.ID)
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 1 {
		t.Fatalf("leases len: got %d, want 1", len(leases))
	}
	if leases[0].NodeTag != "OldSub/a" {
		t.Fatalf("node_tag: got %q, want %q", leases[0].NodeTag, "OldSub/a")
	}
}

func TestInheritLeaseByPlatformName_Success(t *testing.T) {
	cp, plat := newLeaseInheritanceTestService()

	hash := node.HashFromRawOptions([]byte(`{"id":"parent-node"}`)).Hex()
	now := time.Now().UnixNano()
	parent := model.Lease{
		PlatformID:     plat.ID,
		Account:        "parent",
		NodeHash:       hash,
		EgressIP:       "203.0.113.10",
		CreatedAtNs:    now - int64(5*time.Minute),
		ExpiryNs:       now + int64(30*time.Minute),
		LastAccessedNs: now - int64(time.Minute),
	}
	seedLease(t, cp, parent)

	if err := cp.InheritLeaseByPlatformName(plat.Name, "parent", "child"); err != nil {
		t.Fatalf("InheritLeaseByPlatformName: %v", err)
	}

	child := cp.Router.ReadLease(model.LeaseKey{PlatformID: plat.ID, Account: "child"})
	if child == nil {
		t.Fatal("expected inherited lease for child")
	}
	if child.NodeHash != parent.NodeHash {
		t.Fatalf("child node_hash: got %q, want %q", child.NodeHash, parent.NodeHash)
	}
	if child.EgressIP != parent.EgressIP {
		t.Fatalf("child egress_ip: got %q, want %q", child.EgressIP, parent.EgressIP)
	}
	if child.ExpiryNs != parent.ExpiryNs {
		t.Fatalf("child expiry_ns: got %d, want %d", child.ExpiryNs, parent.ExpiryNs)
	}
	if child.CreatedAtNs != parent.CreatedAtNs {
		t.Fatalf("child created_at_ns: got %d, want %d", child.CreatedAtNs, parent.CreatedAtNs)
	}
	if child.LastAccessedNs != parent.LastAccessedNs {
		t.Fatalf("child last_accessed_ns: got %d, want %d", child.LastAccessedNs, parent.LastAccessedNs)
	}
}

func TestInheritLeaseByPlatformName_OverwritesExistingTargetLease(t *testing.T) {
	cp, plat := newLeaseInheritanceTestService()

	now := time.Now().UnixNano()
	parentHash := node.HashFromRawOptions([]byte(`{"id":"parent-node-overwrite"}`)).Hex()
	oldChildHash := node.HashFromRawOptions([]byte(`{"id":"old-child-node"}`)).Hex()

	parent := model.Lease{
		PlatformID:     plat.ID,
		Account:        "parent",
		NodeHash:       parentHash,
		EgressIP:       "198.51.100.1",
		CreatedAtNs:    now - int64(2*time.Minute),
		ExpiryNs:       now + int64(20*time.Minute),
		LastAccessedNs: now - int64(10*time.Second),
	}
	oldChild := model.Lease{
		PlatformID:     plat.ID,
		Account:        "child",
		NodeHash:       oldChildHash,
		EgressIP:       "198.51.100.2",
		CreatedAtNs:    now - int64(time.Minute),
		ExpiryNs:       now + int64(5*time.Minute),
		LastAccessedNs: now - int64(5*time.Second),
	}
	seedLease(t, cp, parent)
	seedLease(t, cp, oldChild)

	if err := cp.InheritLeaseByPlatformName(plat.Name, "parent", "child"); err != nil {
		t.Fatalf("InheritLeaseByPlatformName: %v", err)
	}

	child := cp.Router.ReadLease(model.LeaseKey{PlatformID: plat.ID, Account: "child"})
	if child == nil {
		t.Fatal("expected overwritten child lease")
	}
	if child.NodeHash != parent.NodeHash {
		t.Fatalf("child node_hash after overwrite: got %q, want %q", child.NodeHash, parent.NodeHash)
	}
	if child.EgressIP != parent.EgressIP {
		t.Fatalf("child egress_ip after overwrite: got %q, want %q", child.EgressIP, parent.EgressIP)
	}
	if child.ExpiryNs != parent.ExpiryNs {
		t.Fatalf("child expiry_ns after overwrite: got %d, want %d", child.ExpiryNs, parent.ExpiryNs)
	}
}

func TestInheritLeaseByPlatformName_ParentLeaseMissingOrExpired(t *testing.T) {
	cp, plat := newLeaseInheritanceTestService()

	err := cp.InheritLeaseByPlatformName(plat.Name, "missing-parent", "child")
	if err == nil {
		t.Fatal("expected NOT_FOUND for missing parent lease")
	}
	assertServiceErrorCode(t, err, "NOT_FOUND")

	now := time.Now().UnixNano()
	expiredParent := model.Lease{
		PlatformID:     plat.ID,
		Account:        "expired-parent",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"expired-parent-node"}`)).Hex(),
		EgressIP:       "203.0.113.77",
		CreatedAtNs:    now - int64(time.Hour),
		ExpiryNs:       now - int64(time.Second),
		LastAccessedNs: now - int64(time.Minute),
	}
	seedLease(t, cp, expiredParent)

	err = cp.InheritLeaseByPlatformName(plat.Name, "expired-parent", "child")
	if err == nil {
		t.Fatal("expected NOT_FOUND for expired parent lease")
	}
	assertServiceErrorCode(t, err, "NOT_FOUND")
}

func TestInheritLeaseByPlatformName_InvalidArguments(t *testing.T) {
	cp, _ := newLeaseInheritanceTestService()

	cases := []struct {
		name         string
		platformName string
		parent       string
		child        string
	}{
		{
			name:         "empty platform",
			platformName: "",
			parent:       "parent",
			child:        "child",
		},
		{
			name:         "empty parent",
			platformName: "Default",
			parent:       "   ",
			child:        "child",
		},
		{
			name:         "empty child",
			platformName: "Default",
			parent:       "parent",
			child:        "   ",
		},
		{
			name:         "same parent and child",
			platformName: "Default",
			parent:       "same",
			child:        "same",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cp.InheritLeaseByPlatformName(tc.platformName, tc.parent, tc.child)
			if err == nil {
				t.Fatal("expected INVALID_ARGUMENT error")
			}
			assertServiceErrorCode(t, err, "INVALID_ARGUMENT")
		})
	}
}
