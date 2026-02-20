package main

import (
	"encoding/json"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/state"
	"github.com/resin-proxy/resin/internal/topology"
)

func newBootstrapTestRuntime(runtimeCfg *config.RuntimeConfig) (*topology.SubscriptionManager, *topology.GlobalNodePool) {
	subManager := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subManager.Lookup,
		GeoLookup:              func(netip.Addr) string { return "" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return runtimeCfg.MaxConsecutiveFailures },
		LatencyDecayWindow: func() time.Duration {
			return time.Duration(runtimeCfg.LatencyDecayWindow)
		},
	})
	return subManager, pool
}

func newDefaultPlatformEnvConfig() *config.EnvConfig {
	return &config.EnvConfig{
		DefaultPlatformStickyTTL:              7 * 24 * time.Hour,
		DefaultPlatformRegexFilters:           []string{},
		DefaultPlatformRegionFilters:          []string{},
		DefaultPlatformReverseProxyMissAction: "RANDOM",
		DefaultPlatformAllocationPolicy:       "BALANCED",
	}
}

func TestBootstrapTopology_CreatesDefaultPlatformWhenMissing(t *testing.T) {
	engine, closer, err := state.PersistenceBootstrap(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	runtimeCfg := config.NewDefaultRuntimeConfig()
	envCfg := newDefaultPlatformEnvConfig()
	envCfg.DefaultPlatformStickyTTL = 2 * time.Hour
	envCfg.DefaultPlatformRegexFilters = []string{`^Provider/.*`}
	envCfg.DefaultPlatformRegionFilters = []string{"us", "hk"}
	envCfg.DefaultPlatformReverseProxyMissAction = "REJECT"
	envCfg.DefaultPlatformAllocationPolicy = "PREFER_LOW_LATENCY"

	subManager, pool := newBootstrapTestRuntime(runtimeCfg)
	if err := bootstrapTopology(engine, subManager, pool, envCfg); err != nil {
		t.Fatalf("bootstrapTopology: %v", err)
	}

	platforms, err := engine.ListPlatforms()
	if err != nil {
		t.Fatalf("ListPlatforms: %v", err)
	}
	if len(platforms) != 1 {
		t.Fatalf("expected 1 platform, got %d", len(platforms))
	}

	defaultPlat := platforms[0]
	if defaultPlat.ID != platform.DefaultPlatformID {
		t.Fatalf("default id: got %q, want %q", defaultPlat.ID, platform.DefaultPlatformID)
	}
	if defaultPlat.Name != platform.DefaultPlatformName {
		t.Fatalf("default name: got %q, want %q", defaultPlat.Name, platform.DefaultPlatformName)
	}
	if defaultPlat.StickyTTLNs != int64(2*time.Hour) {
		t.Fatalf("sticky_ttl_ns: got %d, want %d", defaultPlat.StickyTTLNs, int64(2*time.Hour))
	}
	if defaultPlat.ReverseProxyMissAction != "REJECT" {
		t.Fatalf("reverse_proxy_miss_action: got %q, want %q", defaultPlat.ReverseProxyMissAction, "REJECT")
	}
	if defaultPlat.AllocationPolicy != "PREFER_LOW_LATENCY" {
		t.Fatalf("allocation_policy: got %q, want %q", defaultPlat.AllocationPolicy, "PREFER_LOW_LATENCY")
	}

	if !reflect.DeepEqual(defaultPlat.RegexFilters, []string{`^Provider/.*`}) {
		t.Fatalf("regex_filters: got %v", defaultPlat.RegexFilters)
	}
	if !reflect.DeepEqual(defaultPlat.RegionFilters, []string{"us", "hk"}) {
		t.Fatalf("region_filters: got %v", defaultPlat.RegionFilters)
	}

	if _, ok := pool.GetPlatform(platform.DefaultPlatformID); !ok {
		t.Fatal("default platform should be registered in pool by ID")
	}
	if _, ok := pool.GetPlatformByName(platform.DefaultPlatformName); !ok {
		t.Fatal("default platform should be registered in pool by name")
	}
}

func TestBootstrapTopology_DefaultPlatformCreationIsIdempotent(t *testing.T) {
	engine, closer, err := state.PersistenceBootstrap(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	runtimeCfg := config.NewDefaultRuntimeConfig()
	envCfg := newDefaultPlatformEnvConfig()
	subManager, pool := newBootstrapTestRuntime(runtimeCfg)

	if err := bootstrapTopology(engine, subManager, pool, envCfg); err != nil {
		t.Fatalf("first bootstrapTopology: %v", err)
	}
	if err := bootstrapTopology(engine, subManager, pool, envCfg); err != nil {
		t.Fatalf("second bootstrapTopology: %v", err)
	}

	platforms, err := engine.ListPlatforms()
	if err != nil {
		t.Fatalf("ListPlatforms: %v", err)
	}
	if len(platforms) != 1 {
		t.Fatalf("expected exactly 1 platform after repeated bootstrap, got %d", len(platforms))
	}
	if platforms[0].ID != platform.DefaultPlatformID {
		t.Fatalf("unexpected platform id after repeated bootstrap: %q", platforms[0].ID)
	}
}

func TestBootstrapTopology_DefaultPlatformByNameDoesNotSatisfyDefaultID(t *testing.T) {
	engine, closer, err := state.PersistenceBootstrap(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	now := time.Now().UnixNano()
	if err := engine.UpsertPlatform(model.Platform{
		ID:                     "legacy-default-id",
		Name:                   platform.DefaultPlatformName,
		StickyTTLNs:            int64(time.Hour),
		RegexFilters:           []string{},
		RegionFilters:          []string{},
		ReverseProxyMissAction: "RANDOM",
		AllocationPolicy:       "BALANCED",
		UpdatedAtNs:            now,
	}); err != nil {
		t.Fatalf("seed legacy default-by-name platform: %v", err)
	}

	subManager, pool := newBootstrapTestRuntime(config.NewDefaultRuntimeConfig())
	err = bootstrapTopology(engine, subManager, pool, newDefaultPlatformEnvConfig())
	if err == nil {
		t.Fatal("expected bootstrapTopology to fail when default ID is missing but default name is occupied")
	}
	if !strings.Contains(err.Error(), "ensure default platform") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "platform name already exists") {
		t.Fatalf("unexpected error detail: %v", err)
	}
}

func TestBootstrapTopology_FailsFastOnCorruptPlatformFilters(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	cacheDir := filepath.Join(root, "cache")

	engine, closer, err := state.PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	now := time.Now().UnixNano()
	if err := engine.UpsertPlatform(model.Platform{
		ID:                     "plat-1",
		Name:                   "BrokenOnRead",
		StickyTTLNs:            int64(time.Hour),
		RegexFilters:           []string{`^ok$`},
		RegionFilters:          []string{"us"},
		ReverseProxyMissAction: "RANDOM",
		AllocationPolicy:       "BALANCED",
		UpdatedAtNs:            now,
	}); err != nil {
		t.Fatalf("UpsertPlatform: %v", err)
	}

	db, err := state.OpenDB(filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatalf("OpenDB(state.db): %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(
		`UPDATE platforms SET regex_filters_json = ? WHERE id = ?`,
		`["(broken"]`,
		"plat-1",
	); err != nil {
		t.Fatalf("corrupt platform row: %v", err)
	}

	subManager, pool := newBootstrapTestRuntime(config.NewDefaultRuntimeConfig())
	err = bootstrapTopology(engine, subManager, pool, newDefaultPlatformEnvConfig())
	if err == nil {
		t.Fatal("expected bootstrapTopology to fail on corrupt platform filters")
	}
	if !strings.Contains(err.Error(), "regex_filters") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMarkNodeRemovedDirty_DeletesStaticDynamicAndLatency(t *testing.T) {
	engine, closer, err := state.PersistenceBootstrap(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	raw := json.RawMessage(`{"type":"stub","server":"198.51.100.42","server_port":443}`)
	hash := node.HashFromRawOptions(raw)
	hashHex := hash.Hex()

	entry := node.NewNodeEntry(hash, raw, time.Now(), 16)
	entry.FailureCount.Store(2)
	entry.CircuitOpenSince.Store(time.Now().Add(-time.Minute).UnixNano())
	entry.SetEgressIP(netip.MustParseAddr("203.0.113.50"))
	entry.LastEgressUpdate.Store(time.Now().UnixNano())
	entry.LastEgressUpdateAttempt.Store(time.Now().UnixNano())
	entry.LastLatencyProbeAttempt.Store(time.Now().UnixNano())
	entry.LastAuthorityLatencyProbeAttempt.Store(time.Now().UnixNano())
	entry.LatencyTable.Update("example.com", 55*time.Millisecond, 5*time.Minute)
	entry.LatencyTable.Update("cloudflare.com", 65*time.Millisecond, 5*time.Minute)

	readers := state.CacheReaders{
		ReadNodeStatic: func(h string) *model.NodeStatic {
			if h != hashHex {
				return nil
			}
			return &model.NodeStatic{
				Hash:        hashHex,
				RawOptions:  append(json.RawMessage(nil), raw...),
				CreatedAtNs: entry.CreatedAt.UnixNano(),
			}
		},
		ReadNodeDynamic: func(h string) *model.NodeDynamic {
			if h != hashHex {
				return nil
			}
			return &model.NodeDynamic{
				Hash:                               hashHex,
				FailureCount:                       int(entry.FailureCount.Load()),
				CircuitOpenSince:                   entry.CircuitOpenSince.Load(),
				EgressIP:                           entry.GetEgressIP().String(),
				EgressUpdatedAtNs:                  entry.LastEgressUpdate.Load(),
				LastLatencyProbeAttemptNs:          entry.LastLatencyProbeAttempt.Load(),
				LastAuthorityLatencyProbeAttemptNs: entry.LastAuthorityLatencyProbeAttempt.Load(),
				LastEgressUpdateAttemptNs:          entry.LastEgressUpdateAttempt.Load(),
			}
		},
		ReadNodeLatency: func(key model.NodeLatencyKey) *model.NodeLatency {
			if key.NodeHash != hashHex {
				return nil
			}
			stats, ok := entry.LatencyTable.GetDomainStats(key.Domain)
			if !ok {
				return nil
			}
			return &model.NodeLatency{
				NodeHash:      hashHex,
				Domain:        key.Domain,
				EwmaNs:        int64(stats.Ewma),
				LastUpdatedNs: stats.LastUpdated.UnixNano(),
			}
		},
	}

	// Seed cache rows for this node.
	engine.MarkNodeStatic(hashHex)
	engine.MarkNodeDynamic(hashHex)
	engine.MarkNodeLatency(hashHex, "example.com")
	engine.MarkNodeLatency(hashHex, "cloudflare.com")
	if err := engine.FlushDirtySets(readers); err != nil {
		t.Fatalf("seed FlushDirtySets: %v", err)
	}

	// Simulate node removed callback and flush deletes.
	markNodeRemovedDirty(engine, hash, entry)
	if err := engine.FlushDirtySets(state.CacheReaders{}); err != nil {
		t.Fatalf("delete FlushDirtySets: %v", err)
	}

	nodesStatic, err := engine.LoadAllNodesStatic()
	if err != nil {
		t.Fatalf("LoadAllNodesStatic: %v", err)
	}
	if len(nodesStatic) != 0 {
		t.Fatalf("nodes_static not deleted: %+v", nodesStatic)
	}

	nodesDynamic, err := engine.LoadAllNodesDynamic()
	if err != nil {
		t.Fatalf("LoadAllNodesDynamic: %v", err)
	}
	if len(nodesDynamic) != 0 {
		t.Fatalf("nodes_dynamic not deleted: %+v", nodesDynamic)
	}

	latencies, err := engine.LoadAllNodeLatency()
	if err != nil {
		t.Fatalf("LoadAllNodeLatency: %v", err)
	}
	if len(latencies) != 0 {
		t.Fatalf("node_latency not deleted: %+v", latencies)
	}
}
