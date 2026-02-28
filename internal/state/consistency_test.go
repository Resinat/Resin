package state

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
)

func TestRepairConsistency_RemovesOrphans(t *testing.T) {
	stateDir := t.TempDir()
	cacheDir := t.TempDir()

	stateDBPath := filepath.Join(stateDir, "state.db")
	cacheDBPath := filepath.Join(cacheDir, "cache.db")

	// Set up state.db with one platform and one subscription.
	sdb, err := OpenDB(stateDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	if err := MigrateStateDB(sdb); err != nil {
		t.Fatal(err)
	}

	stateRepo := newStateRepo(sdb)
	stateRepo.UpsertPlatform(model.Platform{
		ID: "p1", Name: "P1", StickyTTLNs: 1000,
		RegexFilters: []string{}, RegionFilters: []string{},
		ReverseProxyMissAction: "RANDOM", AllocationPolicy: "BALANCED",
		UpdatedAtNs: 1,
	})
	stateRepo.UpsertSubscription(model.Subscription{
		ID: "s1", Name: "S1", URL: "https://example.com",
		UpdateIntervalNs: 30_000_000_000, Enabled: true, Ephemeral: false,
		EphemeralNodeEvictDelayNs: int64(72 * time.Hour), CreatedAtNs: 1, UpdatedAtNs: 1,
	})

	// Set up cache.db with valid + orphan records.
	cdb, err := OpenDB(cacheDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cdb.Close()
	if err := MigrateCacheDB(cdb); err != nil {
		t.Fatal(err)
	}

	cacheRepo := newCacheRepo(cdb)

	// Valid node (referenced by valid subscription_node).
	cacheRepo.BulkUpsertNodesStatic([]model.NodeStatic{
		{Hash: "valid-node", RawOptions: json.RawMessage(`{}`), CreatedAtNs: 1},
		{Hash: "orphan-node", RawOptions: json.RawMessage(`{}`), CreatedAtNs: 2}, // no subscription_node ref
		{Hash: "evicted-only-node", RawOptions: json.RawMessage(`{}`), CreatedAtNs: 3},
	})
	cacheRepo.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{
		{SubscriptionID: "s1", NodeHash: "valid-node", Tags: []string{}},               // valid
		{SubscriptionID: "s-missing", NodeHash: "valid-node", Tags: []string{}},        // orphan: sub doesn't exist
		{SubscriptionID: "s1", NodeHash: "node-missing-from-static", Tags: []string{}}, // orphan: node doesn't exist in static
		{SubscriptionID: "s1", NodeHash: "evicted-only-node", Tags: []string{"x"}, Evicted: true},
		{SubscriptionID: "s1", NodeHash: "evicted-missing-static", Tags: []string{"y"}, Evicted: true},
	})
	cacheRepo.BulkUpsertNodesDynamic([]model.NodeDynamic{
		{Hash: "valid-node"},
		{Hash: "orphan-dynamic"}, // no static ref
	})
	cacheRepo.BulkUpsertNodeLatency([]model.NodeLatency{
		{NodeHash: "valid-node", Domain: "google.com", EwmaNs: 100, LastUpdatedNs: 1},
		{NodeHash: "orphan-latency-node", Domain: "google.com", EwmaNs: 200, LastUpdatedNs: 1}, // no static ref
	})
	cacheRepo.BulkUpsertLeases([]model.Lease{
		{PlatformID: "p1", Account: "user1", NodeHash: "valid-node", ExpiryNs: 9999, LastAccessedNs: 1},        // valid
		{PlatformID: "p-missing", Account: "user2", NodeHash: "valid-node", ExpiryNs: 9999, LastAccessedNs: 1}, // orphan: platform missing
		{PlatformID: "p1", Account: "user3", NodeHash: "node-gone", ExpiryNs: 9999, LastAccessedNs: 1},         // orphan: node missing
	})

	// Run repair.
	if err := RepairConsistency(stateDBPath, cdb); err != nil {
		t.Fatal(err)
	}

	// Verify subscription_nodes:
	//   - (s1,valid-node) survives.
	//   - evicted rows survive even when nodes_static is missing.
	sns, _ := cacheRepo.LoadAllSubscriptionNodes()
	if len(sns) != 3 {
		t.Fatalf("expected 3 subscription_node rows, got %d: %+v", len(sns), sns)
	}
	gotRows := make(map[string]model.SubscriptionNode, len(sns))
	for _, sn := range sns {
		gotRows[sn.NodeHash] = sn
	}
	if _, ok := gotRows["valid-node"]; !ok {
		t.Fatalf("expected valid-node sub row to survive: %+v", sns)
	}
	if sn, ok := gotRows["evicted-only-node"]; !ok || !sn.Evicted {
		t.Fatalf("expected evicted-only-node row to survive as evicted: %+v", sns)
	}
	if sn, ok := gotRows["evicted-missing-static"]; !ok || !sn.Evicted {
		t.Fatalf("expected evicted-missing-static row to survive as evicted: %+v", sns)
	}

	// Verify nodes_static: only "valid-node" survives.
	// "evicted-only-node" is deleted because it has no non-evicted reference.
	nodes, _ := cacheRepo.LoadAllNodesStatic()
	if len(nodes) != 1 || nodes[0].Hash != "valid-node" {
		t.Fatalf("expected only valid-node, got %+v", nodes)
	}

	// Verify nodes_dynamic: only "valid-node" survives.
	dyn, _ := cacheRepo.LoadAllNodesDynamic()
	if len(dyn) != 1 || dyn[0].Hash != "valid-node" {
		t.Fatalf("expected only valid-node dynamic, got %+v", dyn)
	}

	// Verify node_latency: only valid-node's latency survives.
	lat, _ := cacheRepo.LoadAllNodeLatency()
	if len(lat) != 1 || lat[0].NodeHash != "valid-node" {
		t.Fatalf("expected only valid-node latency, got %+v", lat)
	}

	// Verify leases: only (p1, user1) survives.
	leases, _ := cacheRepo.LoadAllLeases()
	if len(leases) != 1 || leases[0].Account != "user1" {
		t.Fatalf("expected only user1 lease, got %+v", leases)
	}
}

func TestRepairConsistency_ValidRecordsSurvive(t *testing.T) {
	stateDir := t.TempDir()
	cacheDir := t.TempDir()

	stateDBPath := filepath.Join(stateDir, "state.db")
	cacheDBPath := filepath.Join(cacheDir, "cache.db")

	sdb, _ := OpenDB(stateDBPath)
	defer sdb.Close()
	if err := MigrateStateDB(sdb); err != nil {
		t.Fatal(err)
	}

	stateRepo := newStateRepo(sdb)
	stateRepo.UpsertPlatform(model.Platform{
		ID: "p1", Name: "P1", StickyTTLNs: 1000,
		RegexFilters: []string{}, RegionFilters: []string{},
		ReverseProxyMissAction: "RANDOM", AllocationPolicy: "BALANCED",
		UpdatedAtNs: 1,
	})
	stateRepo.UpsertSubscription(model.Subscription{
		ID: "s1", Name: "S1", URL: "https://example.com",
		UpdateIntervalNs: 30_000_000_000, Enabled: true, Ephemeral: false,
		EphemeralNodeEvictDelayNs: int64(72 * time.Hour), CreatedAtNs: 1, UpdatedAtNs: 1,
	})

	cdb, _ := OpenDB(cacheDBPath)
	defer cdb.Close()
	if err := MigrateCacheDB(cdb); err != nil {
		t.Fatal(err)
	}

	cacheRepo := newCacheRepo(cdb)
	cacheRepo.BulkUpsertNodesStatic([]model.NodeStatic{
		{Hash: "n1", RawOptions: json.RawMessage(`{}`), CreatedAtNs: 1},
	})
	cacheRepo.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{
		{SubscriptionID: "s1", NodeHash: "n1", Tags: []string{"t1"}},
	})
	cacheRepo.BulkUpsertNodesDynamic([]model.NodeDynamic{
		{Hash: "n1", FailureCount: 1},
	})
	cacheRepo.BulkUpsertNodeLatency([]model.NodeLatency{
		{NodeHash: "n1", Domain: "example.com", EwmaNs: 500, LastUpdatedNs: 1},
	})
	cacheRepo.BulkUpsertLeases([]model.Lease{
		{PlatformID: "p1", Account: "a1", NodeHash: "n1", ExpiryNs: 9999, LastAccessedNs: 1},
	})

	// Run repair — nothing should be deleted.
	RepairConsistency(stateDBPath, cdb)

	nodes, _ := cacheRepo.LoadAllNodesStatic()
	sns, _ := cacheRepo.LoadAllSubscriptionNodes()
	dyn, _ := cacheRepo.LoadAllNodesDynamic()
	lat, _ := cacheRepo.LoadAllNodeLatency()
	leases, _ := cacheRepo.LoadAllLeases()

	if len(nodes) != 1 || len(sns) != 1 || len(dyn) != 1 || len(lat) != 1 || len(leases) != 1 {
		t.Fatalf("valid records should survive: nodes=%d sns=%d dyn=%d lat=%d leases=%d",
			len(nodes), len(sns), len(dyn), len(lat), len(leases))
	}
}
