package routing

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/platform"
)

func TestUpsertLease_CreateAndReplace(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-upsert", "Plat-Upsert", nil, nil)
	pool.addPlatform(plat)

	var events []LeaseEvent
	router := newTestRouter(pool, func(e LeaseEvent) {
		events = append(events, e)
	})

	hashA, _ := newRoutableEntry(t, `{"id":"upsert-node-a"}`, "198.51.100.10")
	hashB, _ := newRoutableEntry(t, `{"id":"upsert-node-b"}`, "198.51.100.11")

	nowNs := time.Now().UnixNano()
	first := model.Lease{
		PlatformID:     plat.ID,
		Account:        "acct-1",
		NodeHash:       hashA.Hex(),
		EgressIP:       "203.0.113.10",
		CreatedAtNs:    nowNs - int64(time.Minute),
		ExpiryNs:       nowNs + int64(time.Hour),
		LastAccessedNs: nowNs - int64(5*time.Second),
	}
	if err := router.UpsertLease(first); err != nil {
		t.Fatalf("UpsertLease(create): %v", err)
	}

	got := router.ReadLease(model.LeaseKey{PlatformID: plat.ID, Account: "acct-1"})
	if got == nil {
		t.Fatal("expected lease after create")
	}
	if got.NodeHash != first.NodeHash {
		t.Fatalf("created lease node_hash: got %q, want %q", got.NodeHash, first.NodeHash)
	}
	if got.EgressIP != first.EgressIP {
		t.Fatalf("created lease egress_ip: got %q, want %q", got.EgressIP, first.EgressIP)
	}

	snapshot := router.SnapshotIPLoad(plat.ID)
	if snapshot[netip.MustParseAddr("203.0.113.10")] != 1 {
		t.Fatalf("ip load for first IP: got %d, want 1", snapshot[netip.MustParseAddr("203.0.113.10")])
	}
	if len(events) != 1 || events[0].Type != LeaseCreate {
		t.Fatalf("events after create: got %+v, want first event LeaseCreate", events)
	}

	second := model.Lease{
		PlatformID:     plat.ID,
		Account:        "acct-1",
		NodeHash:       hashB.Hex(),
		EgressIP:       "203.0.113.11",
		CreatedAtNs:    nowNs - int64(30*time.Second),
		ExpiryNs:       nowNs + int64(2*time.Hour),
		LastAccessedNs: nowNs,
	}
	if err := router.UpsertLease(second); err != nil {
		t.Fatalf("UpsertLease(replace): %v", err)
	}

	got = router.ReadLease(model.LeaseKey{PlatformID: plat.ID, Account: "acct-1"})
	if got == nil {
		t.Fatal("expected lease after replace")
	}
	if got.NodeHash != second.NodeHash {
		t.Fatalf("replaced lease node_hash: got %q, want %q", got.NodeHash, second.NodeHash)
	}
	if got.EgressIP != second.EgressIP {
		t.Fatalf("replaced lease egress_ip: got %q, want %q", got.EgressIP, second.EgressIP)
	}
	if got.ExpiryNs != second.ExpiryNs {
		t.Fatalf("replaced lease expiry_ns: got %d, want %d", got.ExpiryNs, second.ExpiryNs)
	}

	snapshot = router.SnapshotIPLoad(plat.ID)
	if _, ok := snapshot[netip.MustParseAddr("203.0.113.10")]; ok {
		t.Fatalf("old IP should not have positive lease count after replace: snapshot=%v", snapshot)
	}
	if snapshot[netip.MustParseAddr("203.0.113.11")] != 1 {
		t.Fatalf("ip load for second IP: got %d, want 1", snapshot[netip.MustParseAddr("203.0.113.11")])
	}
	if len(events) != 2 || events[1].Type != LeaseReplace {
		t.Fatalf("events after replace: got %+v, want second event LeaseReplace", events)
	}
}

func TestUpsertLease_Validation(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-upsert-validate", "Plat-Upsert-Validate", nil, nil)
	pool.addPlatform(plat)
	router := newTestRouter(pool, nil)

	cases := []struct {
		name  string
		lease model.Lease
	}{
		{
			name: "missing platform id",
			lease: model.Lease{
				PlatformID: "",
				Account:    "acct",
				NodeHash:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				EgressIP:   "203.0.113.1",
			},
		},
		{
			name: "missing account",
			lease: model.Lease{
				PlatformID: plat.ID,
				Account:    "   ",
				NodeHash:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				EgressIP:   "203.0.113.1",
			},
		},
		{
			name: "invalid node hash",
			lease: model.Lease{
				PlatformID: plat.ID,
				Account:    "acct",
				NodeHash:   "not-a-hash",
				EgressIP:   "203.0.113.1",
			},
		},
		{
			name: "invalid egress ip",
			lease: model.Lease{
				PlatformID: plat.ID,
				Account:    "acct",
				NodeHash:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				EgressIP:   "not-an-ip",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := router.UpsertLease(tc.lease); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
