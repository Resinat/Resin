package state

import (
	"testing"

	"github.com/resin-proxy/resin/internal/model"
)

func newTestCacheRepo(t *testing.T) *CacheRepo {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenDB(dir + "/cache.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := InitDB(db, CreateCacheDDL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return newCacheRepo(db)
}

// --- nodes_static ---

func TestCacheRepo_NodesStatic_BulkUpsertAndLoad(t *testing.T) {
	repo := newTestCacheRepo(t)

	nodes := []model.NodeStatic{
		{Hash: "aaa", RawOptionsJSON: `{"type":"ss"}`, CreatedAtNs: 100},
		{Hash: "bbb", RawOptionsJSON: `{"type":"vmess"}`, CreatedAtNs: 200},
	}
	if err := repo.BulkUpsertNodesStatic(nodes); err != nil {
		t.Fatal(err)
	}

	loaded, err := repo.LoadAllNodesStatic()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(loaded))
	}

	// Idempotent upsert: update existing.
	nodes[0].RawOptionsJSON = `{"type":"ss","updated":true}`
	if err := repo.BulkUpsertNodesStatic(nodes[:1]); err != nil {
		t.Fatal(err)
	}
	loaded, _ = repo.LoadAllNodesStatic()
	for _, n := range loaded {
		if n.Hash == "aaa" && n.RawOptionsJSON != `{"type":"ss","updated":true}` {
			t.Fatalf("expected updated options, got %s", n.RawOptionsJSON)
		}
	}
}

func TestCacheRepo_NodesStatic_BulkDelete(t *testing.T) {
	repo := newTestCacheRepo(t)

	nodes := []model.NodeStatic{
		{Hash: "aaa", RawOptionsJSON: "{}", CreatedAtNs: 100},
		{Hash: "bbb", RawOptionsJSON: "{}", CreatedAtNs: 200},
	}
	repo.BulkUpsertNodesStatic(nodes)

	if err := repo.BulkDeleteNodesStatic([]string{"aaa"}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := repo.LoadAllNodesStatic()
	if len(loaded) != 1 || loaded[0].Hash != "bbb" {
		t.Fatalf("expected only bbb, got %+v", loaded)
	}
}

// --- nodes_dynamic ---

func TestCacheRepo_NodesDynamic_BulkUpsertAndLoad(t *testing.T) {
	repo := newTestCacheRepo(t)

	nodes := []model.NodeDynamic{
		{Hash: "aaa", FailureCount: 3, CircuitOpenSince: 1000, EgressIP: "1.2.3.4", EgressUpdatedAtNs: 500},
	}
	if err := repo.BulkUpsertNodesDynamic(nodes); err != nil {
		t.Fatal(err)
	}

	loaded, err := repo.LoadAllNodesDynamic()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].FailureCount != 3 {
		t.Fatalf("unexpected: %+v", loaded)
	}

	// Update.
	nodes[0].FailureCount = 0
	repo.BulkUpsertNodesDynamic(nodes)
	loaded, _ = repo.LoadAllNodesDynamic()
	if loaded[0].FailureCount != 0 {
		t.Fatalf("expected 0 failures after reset, got %d", loaded[0].FailureCount)
	}
}

func TestCacheRepo_NodesDynamic_BulkDelete(t *testing.T) {
	repo := newTestCacheRepo(t)

	repo.BulkUpsertNodesDynamic([]model.NodeDynamic{{Hash: "aaa"}, {Hash: "bbb"}})
	repo.BulkDeleteNodesDynamic([]string{"bbb"})

	loaded, _ := repo.LoadAllNodesDynamic()
	if len(loaded) != 1 || loaded[0].Hash != "aaa" {
		t.Fatalf("expected only aaa, got %+v", loaded)
	}
}

// --- node_latency ---

func TestCacheRepo_NodeLatency_BulkUpsertAndLoad(t *testing.T) {
	repo := newTestCacheRepo(t)

	entries := []model.NodeLatency{
		{NodeHash: "aaa", Domain: "google.com", EwmaNs: 5000, LastUpdatedNs: 100},
		{NodeHash: "aaa", Domain: "github.com", EwmaNs: 8000, LastUpdatedNs: 200},
	}
	if err := repo.BulkUpsertNodeLatency(entries); err != nil {
		t.Fatal(err)
	}

	loaded, err := repo.LoadAllNodeLatency()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2, got %d", len(loaded))
	}
}

func TestCacheRepo_NodeLatency_BulkDelete(t *testing.T) {
	repo := newTestCacheRepo(t)

	repo.BulkUpsertNodeLatency([]model.NodeLatency{
		{NodeHash: "aaa", Domain: "google.com", EwmaNs: 5000, LastUpdatedNs: 100},
		{NodeHash: "aaa", Domain: "github.com", EwmaNs: 8000, LastUpdatedNs: 200},
	})

	repo.BulkDeleteNodeLatency([]model.NodeLatencyKey{{NodeHash: "aaa", Domain: "google.com"}})
	loaded, _ := repo.LoadAllNodeLatency()
	if len(loaded) != 1 || loaded[0].Domain != "github.com" {
		t.Fatalf("expected only github.com, got %+v", loaded)
	}
}

// --- leases ---

func TestCacheRepo_Leases_BulkUpsertAndLoad(t *testing.T) {
	repo := newTestCacheRepo(t)

	leases := []model.Lease{
		{PlatformID: "p1", Account: "user1", NodeHash: "n1", EgressIP: "1.2.3.4", ExpiryNs: 9999, LastAccessedNs: 100},
	}
	if err := repo.BulkUpsertLeases(leases); err != nil {
		t.Fatal(err)
	}

	loaded, err := repo.LoadAllLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Account != "user1" {
		t.Fatalf("unexpected: %+v", loaded)
	}
}

func TestCacheRepo_Leases_BulkDelete(t *testing.T) {
	repo := newTestCacheRepo(t)

	repo.BulkUpsertLeases([]model.Lease{
		{PlatformID: "p1", Account: "user1", NodeHash: "n1", ExpiryNs: 9999, LastAccessedNs: 100},
		{PlatformID: "p1", Account: "user2", NodeHash: "n2", ExpiryNs: 9999, LastAccessedNs: 100},
	})
	repo.BulkDeleteLeases([]model.LeaseKey{{PlatformID: "p1", Account: "user1"}})

	loaded, _ := repo.LoadAllLeases()
	if len(loaded) != 1 || loaded[0].Account != "user2" {
		t.Fatalf("expected only user2, got %+v", loaded)
	}
}

// --- subscription_nodes ---

func TestCacheRepo_SubscriptionNodes_BulkUpsertAndLoad(t *testing.T) {
	repo := newTestCacheRepo(t)

	sns := []model.SubscriptionNode{
		{SubscriptionID: "s1", NodeHash: "n1", TagsJSON: `["tag1","tag2"]`},
		{SubscriptionID: "s1", NodeHash: "n2", TagsJSON: `["tag3"]`},
	}
	if err := repo.BulkUpsertSubscriptionNodes(sns); err != nil {
		t.Fatal(err)
	}

	loaded, err := repo.LoadAllSubscriptionNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2, got %d", len(loaded))
	}

	// Idempotent upsert: update tags.
	sns[0].TagsJSON = `["tag1-updated"]`
	repo.BulkUpsertSubscriptionNodes(sns[:1])
	loaded, _ = repo.LoadAllSubscriptionNodes()
	for _, sn := range loaded {
		if sn.NodeHash == "n1" && sn.TagsJSON != `["tag1-updated"]` {
			t.Fatalf("expected updated tags, got %s", sn.TagsJSON)
		}
	}
}

func TestCacheRepo_SubscriptionNodes_BulkDelete(t *testing.T) {
	repo := newTestCacheRepo(t)

	repo.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{
		{SubscriptionID: "s1", NodeHash: "n1", TagsJSON: "[]"},
		{SubscriptionID: "s1", NodeHash: "n2", TagsJSON: "[]"},
	})
	repo.BulkDeleteSubscriptionNodes([]model.SubscriptionNodeKey{{SubscriptionID: "s1", NodeHash: "n1"}})

	loaded, _ := repo.LoadAllSubscriptionNodes()
	if len(loaded) != 1 || loaded[0].NodeHash != "n2" {
		t.Fatalf("expected only n2, got %+v", loaded)
	}
}

// --- empty bulk operations ---

func TestCacheRepo_BulkEmpty(t *testing.T) {
	repo := newTestCacheRepo(t)

	// All empty bulk operations should be no-ops.
	if err := repo.BulkUpsertNodesStatic(nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.BulkDeleteNodesStatic(nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.BulkUpsertNodesDynamic(nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.BulkDeleteNodesDynamic(nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.BulkUpsertNodeLatency(nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.BulkDeleteNodeLatency(nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.BulkUpsertLeases(nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.BulkDeleteLeases(nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.BulkUpsertSubscriptionNodes(nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.BulkDeleteSubscriptionNodes(nil); err != nil {
		t.Fatal(err)
	}
}

// TestCacheRepo_FlushTx_RollbackOnFailure verifies that if any step inside
// FlushTx fails, the entire transaction is rolled back and no partial writes
// are committed.
func TestCacheRepo_FlushTx_RollbackOnFailure(t *testing.T) {
	repo := newTestCacheRepo(t)

	// Seed: insert a node_static that should survive the failed FlushTx.
	seed := []model.NodeStatic{
		{Hash: "pre-existing", RawOptionsJSON: `{"seed":true}`, CreatedAtNs: 1},
	}
	if err := repo.BulkUpsertNodesStatic(seed); err != nil {
		t.Fatal(err)
	}

	// Drop node_latency table so that the upsert_node_latency step in FlushTx
	// will fail. nodes_static upsert runs first and would succeed in isolation.
	if _, err := repo.db.Exec("DROP TABLE node_latency"); err != nil {
		t.Fatal(err)
	}

	// Build a FlushOps that has work for both nodes_static and node_latency.
	ops := FlushOps{
		UpsertNodesStatic: []model.NodeStatic{
			{Hash: "new-node", RawOptionsJSON: `{"new":true}`, CreatedAtNs: 2},
		},
		UpsertNodeLatency: []model.NodeLatency{
			{NodeHash: "aaa", Domain: "example.com", EwmaNs: 100, LastUpdatedNs: 200},
		},
	}

	err := repo.FlushTx(ops)
	if err == nil {
		t.Fatal("expected FlushTx to fail because node_latency table was dropped")
	}

	// Verify rollback: "new-node" should NOT be committed.
	loaded, err := repo.LoadAllNodesStatic()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 pre-existing node (rollback should prevent new-node), got %d: %+v", len(loaded), loaded)
	}
	if loaded[0].Hash != "pre-existing" {
		t.Fatalf("expected pre-existing node, got %s", loaded[0].Hash)
	}
}
