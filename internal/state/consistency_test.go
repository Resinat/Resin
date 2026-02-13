package state

import (
	"path/filepath"
	"testing"

	"github.com/resin-proxy/resin/internal/model"
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
	InitDB(sdb, CreateStateDDL)

	stateRepo := newStateRepo(sdb)
	stateRepo.UpsertPlatform(model.Platform{
		ID: "p1", Name: "P1", StickyTTLNs: 1000,
		RegexFiltersJSON: "[]", RegionFiltersJSON: "[]",
		ReverseProxyMissAction: "RANDOM", AllocationPolicy: "BALANCED",
		UpdatedAtNs: 1,
	})
	stateRepo.UpsertSubscription(model.Subscription{
		ID: "s1", Name: "S1", URL: "https://example.com",
		UpdateIntervalNs: 1000, Enabled: true, CreatedAtNs: 1, UpdatedAtNs: 1,
	})

	// Set up cache.db with valid + orphan records.
	cdb, err := OpenDB(cacheDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cdb.Close()
	InitDB(cdb, CreateCacheDDL)

	cacheRepo := newCacheRepo(cdb)

	// Valid node (referenced by valid subscription_node).
	cacheRepo.BulkUpsertNodesStatic([]model.NodeStatic{
		{Hash: "valid-node", RawOptionsJSON: `{}`, CreatedAtNs: 1},
		{Hash: "orphan-node", RawOptionsJSON: `{}`, CreatedAtNs: 2}, // no subscription_node ref
	})
	cacheRepo.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{
		{SubscriptionID: "s1", NodeHash: "valid-node", TagsJSON: "[]"},               // valid
		{SubscriptionID: "s-missing", NodeHash: "valid-node", TagsJSON: "[]"},        // orphan: sub doesn't exist
		{SubscriptionID: "s1", NodeHash: "node-missing-from-static", TagsJSON: "[]"}, // orphan: node doesn't exist in static
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

	// Verify subscription_nodes: only (s1, valid-node) survives.
	sns, _ := cacheRepo.LoadAllSubscriptionNodes()
	if len(sns) != 1 {
		t.Fatalf("expected 1 subscription_node, got %d: %+v", len(sns), sns)
	}
	if sns[0].SubscriptionID != "s1" || sns[0].NodeHash != "valid-node" {
		t.Fatalf("wrong surviving sub_node: %+v", sns[0])
	}

	// Verify nodes_static: only "valid-node" survives (orphan-node has no sub_node ref).
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
	InitDB(sdb, CreateStateDDL)

	stateRepo := newStateRepo(sdb)
	stateRepo.UpsertPlatform(model.Platform{
		ID: "p1", Name: "P1", StickyTTLNs: 1000,
		RegexFiltersJSON: "[]", RegionFiltersJSON: "[]",
		ReverseProxyMissAction: "RANDOM", AllocationPolicy: "BALANCED",
		UpdatedAtNs: 1,
	})
	stateRepo.UpsertSubscription(model.Subscription{
		ID: "s1", Name: "S1", URL: "https://example.com",
		UpdateIntervalNs: 1000, Enabled: true, CreatedAtNs: 1, UpdatedAtNs: 1,
	})

	cdb, _ := OpenDB(cacheDBPath)
	defer cdb.Close()
	InitDB(cdb, CreateCacheDDL)

	cacheRepo := newCacheRepo(cdb)
	cacheRepo.BulkUpsertNodesStatic([]model.NodeStatic{
		{Hash: "n1", RawOptionsJSON: `{}`, CreatedAtNs: 1},
	})
	cacheRepo.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{
		{SubscriptionID: "s1", NodeHash: "n1", TagsJSON: `["t1"]`},
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
