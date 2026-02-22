package state

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/model"
)

// newTestEngine sets up a full StateEngine with both DBs in temp dirs.
func newTestEngine(t *testing.T) (*StateEngine, string, string) {
	t.Helper()
	stateDir := t.TempDir()
	cacheDir := t.TempDir()

	engine, closer, err := PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closer.Close() })
	return engine, stateDir, cacheDir
}

// --- Strong persist round-trip ---

func TestEngine_StrongPersist_ConfigSurvivesRestart(t *testing.T) {
	stateDir := t.TempDir()
	cacheDir := t.TempDir()

	// First boot: save config.
	engine1, closer1, err := PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefaultRuntimeConfig()
	cfg.UserAgent = "persist-test"
	if err := engine1.SaveSystemConfig(cfg, 1, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	closer1.Close()

	// Second boot: config should survive.
	engine2, closer2, err := PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer closer2.Close()

	loaded, ver, err := engine2.GetSystemConfig()
	if err != nil {
		t.Fatal(err)
	}
	if ver != 1 || loaded.UserAgent != "persist-test" {
		t.Fatalf("config did not survive restart: ver=%d, ua=%s", ver, loaded.UserAgent)
	}
}

func TestEngine_StrongPersist_PlatformSurvivesRestart(t *testing.T) {
	stateDir := t.TempDir()
	cacheDir := t.TempDir()

	engine1, closer1, err := PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	p := model.Platform{
		ID: "p1", Name: "MyPlatform", StickyTTLNs: 5000,
		RegexFilters: []string{}, RegionFilters: []string{},
		ReverseProxyMissAction: "RANDOM", AllocationPolicy: "BALANCED",
		UpdatedAtNs: time.Now().UnixNano(),
	}
	if err := engine1.UpsertPlatform(p); err != nil {
		t.Fatal(err)
	}
	closer1.Close()

	engine2, closer2, err := PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer closer2.Close()

	got, err := engine2.GetPlatform("p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "MyPlatform" {
		t.Fatalf("platform did not survive: %+v", got)
	}
}

// --- Weak persist restart test ---

func TestEngine_WeakPersist_CacheDataSurvivesRestart(t *testing.T) {
	stateDir := t.TempDir()
	cacheDir := t.TempDir()

	// First boot: set up state.db refs + flush weak data.
	engine1, closer1, err := PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create required state references for consistency repair to keep our data.
	engine1.UpsertSubscription(model.Subscription{
		ID: "s1", Name: "Sub1", URL: "https://example.com",
		UpdateIntervalNs: 30_000_000_000, Enabled: true, CreatedAtNs: 1, UpdatedAtNs: 1,
	})
	engine1.UpsertPlatform(model.Platform{
		ID: "p1", Name: "P1", StickyTTLNs: 1000,
		RegexFilters: []string{}, RegionFilters: []string{},
		ReverseProxyMissAction: "RANDOM", AllocationPolicy: "BALANCED",
		UpdatedAtNs: 1,
	})

	// In-memory stores.
	nodeStore := map[string]*model.NodeStatic{
		"n1": {Hash: "n1", RawOptions: json.RawMessage(`{"type":"ss"}`), CreatedAtNs: 100},
	}
	dynamicStore := map[string]*model.NodeDynamic{
		"n1": {Hash: "n1", FailureCount: 5, EgressIP: "10.0.0.1"},
	}
	subNodeStore := map[model.SubscriptionNodeKey]*model.SubscriptionNode{
		{SubscriptionID: "s1", NodeHash: "n1"}: {SubscriptionID: "s1", NodeHash: "n1", Tags: []string{"fast"}},
	}
	latencyStore := map[model.NodeLatencyKey]*model.NodeLatency{
		{NodeHash: "n1", Domain: "google.com"}: {NodeHash: "n1", Domain: "google.com", EwmaNs: 42000, LastUpdatedNs: 999},
	}
	leaseStore := map[model.LeaseKey]*model.Lease{
		{PlatformID: "p1", Account: "user1"}: {PlatformID: "p1", Account: "user1", NodeHash: "n1", CreatedAtNs: 777, ExpiryNs: 99999, LastAccessedNs: 888},
	}

	readers := CacheReaders{
		ReadNodeStatic:       func(h string) *model.NodeStatic { return nodeStore[h] },
		ReadNodeDynamic:      func(h string) *model.NodeDynamic { return dynamicStore[h] },
		ReadNodeLatency:      func(k NodeLatencyDirtyKey) *model.NodeLatency { return latencyStore[k] },
		ReadLease:            func(k LeaseDirtyKey) *model.Lease { return leaseStore[k] },
		ReadSubscriptionNode: func(k SubscriptionNodeDirtyKey) *model.SubscriptionNode { return subNodeStore[k] },
	}

	engine1.MarkNodeStatic("n1")
	engine1.MarkSubscriptionNode("s1", "n1")
	engine1.MarkNodeDynamic("n1")
	engine1.MarkNodeLatency("n1", "google.com")
	engine1.MarkLease("p1", "user1")
	engine1.FlushDirtySets(readers)
	closer1.Close()

	// Second boot: data should survive restart + consistency repair.
	engine2, closer2, err := PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer closer2.Close()

	nodes, _ := engine2.LoadAllNodesStatic()
	if len(nodes) != 1 || nodes[0].Hash != "n1" {
		t.Fatalf("nodes_static did not survive restart: %+v", nodes)
	}

	dyn, _ := engine2.LoadAllNodesDynamic()
	if len(dyn) != 1 || dyn[0].FailureCount != 5 {
		t.Fatalf("nodes_dynamic did not survive restart: %+v", dyn)
	}

	sns, _ := engine2.LoadAllSubscriptionNodes()
	if len(sns) != 1 || !reflect.DeepEqual(sns[0].Tags, []string{"fast"}) {
		t.Fatalf("subscription_nodes did not survive restart: %+v", sns)
	}

	lat, _ := engine2.LoadAllNodeLatency()
	if len(lat) != 1 || lat[0].EwmaNs != 42000 {
		t.Fatalf("node_latency did not survive restart: %+v", lat)
	}

	leases, _ := engine2.LoadAllLeases()
	if len(leases) != 1 || leases[0].Account != "user1" {
		t.Fatalf("leases did not survive restart: %+v", leases)
	}
	if leases[0].CreatedAtNs != 777 {
		t.Fatalf("lease created_at_ns did not survive restart: %+v", leases)
	}
}

// --- Weak persist: dirty mark → flush → verify ---

func TestEngine_WeakPersist_FlushAndLoad(t *testing.T) {
	engine, _, _ := newTestEngine(t)

	// Simulate in-memory store.
	nodeStore := map[string]*model.NodeStatic{
		"hash-a": {Hash: "hash-a", RawOptions: json.RawMessage(`{"type":"ss"}`), CreatedAtNs: 100},
		"hash-b": {Hash: "hash-b", RawOptions: json.RawMessage(`{"type":"vmess"}`), CreatedAtNs: 200},
	}
	subNodeStore := map[model.SubscriptionNodeKey]*model.SubscriptionNode{
		{SubscriptionID: "s1", NodeHash: "hash-a"}: {SubscriptionID: "s1", NodeHash: "hash-a", Tags: []string{"tag1"}},
	}
	dynamicStore := map[string]*model.NodeDynamic{
		"hash-a": {Hash: "hash-a", FailureCount: 2, EgressIP: "1.1.1.1"},
	}

	readers := CacheReaders{
		ReadNodeStatic:       func(h string) *model.NodeStatic { return nodeStore[h] },
		ReadNodeDynamic:      func(h string) *model.NodeDynamic { return dynamicStore[h] },
		ReadNodeLatency:      func(k NodeLatencyDirtyKey) *model.NodeLatency { return nil },
		ReadLease:            func(k LeaseDirtyKey) *model.Lease { return nil },
		ReadSubscriptionNode: func(k SubscriptionNodeDirtyKey) *model.SubscriptionNode { return subNodeStore[k] },
	}

	// Mark dirty.
	engine.MarkNodeStatic("hash-a")
	engine.MarkNodeStatic("hash-b")
	engine.MarkSubscriptionNode("s1", "hash-a")
	engine.MarkNodeDynamic("hash-a")

	if engine.DirtyCount() != 4 {
		t.Fatalf("expected 4 dirty, got %d", engine.DirtyCount())
	}

	// Flush.
	if err := engine.FlushDirtySets(readers); err != nil {
		t.Fatal(err)
	}

	if engine.DirtyCount() != 0 {
		t.Fatalf("expected 0 dirty after flush, got %d", engine.DirtyCount())
	}

	// Verify in DB.
	nodes, _ := engine.LoadAllNodesStatic()
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes in DB, got %d", len(nodes))
	}

	sns, _ := engine.LoadAllSubscriptionNodes()
	if len(sns) != 1 {
		t.Fatalf("expected 1 sub_node, got %d", len(sns))
	}

	dyn, _ := engine.LoadAllNodesDynamic()
	if len(dyn) != 1 || dyn[0].FailureCount != 2 {
		t.Fatalf("unexpected dynamic: %+v", dyn)
	}
}

func TestEngine_WeakPersist_DeleteFlush(t *testing.T) {
	engine, _, _ := newTestEngine(t)

	nodeStore := map[string]*model.NodeStatic{
		"hash-a": {Hash: "hash-a", RawOptions: json.RawMessage(`{}`), CreatedAtNs: 100},
	}

	readers := CacheReaders{
		ReadNodeStatic:       func(h string) *model.NodeStatic { return nodeStore[h] },
		ReadNodeDynamic:      func(h string) *model.NodeDynamic { return nil },
		ReadNodeLatency:      func(k NodeLatencyDirtyKey) *model.NodeLatency { return nil },
		ReadLease:            func(k LeaseDirtyKey) *model.Lease { return nil },
		ReadSubscriptionNode: func(k SubscriptionNodeDirtyKey) *model.SubscriptionNode { return nil },
	}

	// Insert first.
	engine.MarkNodeStatic("hash-a")
	engine.FlushDirtySets(readers)

	nodes, _ := engine.LoadAllNodesStatic()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}

	// Now delete.
	delete(nodeStore, "hash-a")
	engine.MarkNodeStaticDelete("hash-a")
	engine.FlushDirtySets(readers)

	nodes, _ = engine.LoadAllNodesStatic()
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes after delete flush, got %d", len(nodes))
	}
}

func TestEngine_WeakPersist_UpsertMissTreatedAsDelete(t *testing.T) {
	engine, _, _ := newTestEngine(t)

	// Insert a node first.
	nodeStore := map[string]*model.NodeStatic{
		"hash-a": {Hash: "hash-a", RawOptions: json.RawMessage(`{}`), CreatedAtNs: 100},
	}
	readers := CacheReaders{
		ReadNodeStatic:       func(h string) *model.NodeStatic { return nodeStore[h] },
		ReadNodeDynamic:      func(h string) *model.NodeDynamic { return nil },
		ReadNodeLatency:      func(k NodeLatencyDirtyKey) *model.NodeLatency { return nil },
		ReadLease:            func(k LeaseDirtyKey) *model.Lease { return nil },
		ReadSubscriptionNode: func(k SubscriptionNodeDirtyKey) *model.SubscriptionNode { return nil },
	}

	engine.MarkNodeStatic("hash-a")
	engine.FlushDirtySets(readers)

	// Mark upsert but reader returns nil (object deleted from memory between mark and flush).
	delete(nodeStore, "hash-a")
	engine.MarkNodeStatic("hash-a")
	engine.FlushDirtySets(readers)

	nodes, _ := engine.LoadAllNodesStatic()
	if len(nodes) != 0 {
		t.Fatalf("expected upsert-miss to be treated as delete, got %d nodes", len(nodes))
	}
}

// --- Concurrent Mark + Flush + Restart stability ---

func TestEngine_ConcurrentMarkAndFlush(t *testing.T) {
	engine, _, _ := newTestEngine(t)

	var mu sync.Mutex
	nodeStore := make(map[string]*model.NodeStatic)
	for i := 0; i < 100; i++ {
		h := fmt.Sprintf("node-%d", i)
		nodeStore[h] = &model.NodeStatic{Hash: h, RawOptions: json.RawMessage(`{}`), CreatedAtNs: int64(i)}
	}

	readers := CacheReaders{
		ReadNodeStatic: func(h string) *model.NodeStatic {
			mu.Lock()
			defer mu.Unlock()
			return nodeStore[h]
		},
		ReadNodeDynamic:      func(h string) *model.NodeDynamic { return nil },
		ReadNodeLatency:      func(k NodeLatencyDirtyKey) *model.NodeLatency { return nil },
		ReadLease:            func(k LeaseDirtyKey) *model.Lease { return nil },
		ReadSubscriptionNode: func(k SubscriptionNodeDirtyKey) *model.SubscriptionNode { return nil },
	}

	var wg sync.WaitGroup

	// Writers: mark dirty concurrently.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				engine.MarkNodeStatic(fmt.Sprintf("node-%d", base*10+j))
			}
		}(i)
	}

	// Flushers: flush concurrently.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				engine.FlushDirtySets(readers)
			}
		}()
	}

	wg.Wait()

	// Final flush.
	engine.FlushDirtySets(readers)

	// Verify no data loss: all 100 nodes should be in DB.
	nodes, _ := engine.LoadAllNodesStatic()
	if len(nodes) != 100 {
		t.Fatalf("expected 100 nodes, got %d (some lost in concurrent flush)", len(nodes))
	}
}
