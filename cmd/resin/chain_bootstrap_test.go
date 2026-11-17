package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/state"
	"github.com/Resinat/Resin/internal/topology"
)

func TestBootstrapRestoresChainModeBeforeWarmingOutbounds(t *testing.T) {
	for _, tc := range []struct {
		name               string
		configured, cached bool
	}{
		{"same chain mode", true, true}, {"ordinary cache now chained", true, false}, {"chain cache now ordinary", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, closer, err := state.PersistenceBootstrap(t.TempDir(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer closer.Close()
			if err := engine.UpsertSubscription(model.Subscription{ID: "sub", Name: "sub", URL: "https://example.com", UpdateIntervalNs: int64(time.Minute), Enabled: true, ChainProxy: tc.configured}); err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(`{"type":"http","server":"127.0.0.1","server_port":12345}`)
			hash := node.HashFromRawOptions(raw)
			if err := engine.BulkUpsertNodesStatic([]model.NodeStatic{{Hash: hash.Hex(), RawOptions: raw, CreatedAtNs: time.Now().UnixNano()}}); err != nil {
				t.Fatal(err)
			}
			if err := engine.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{{SubscriptionID: "sub", NodeHash: hash.Hex(), Tags: []string{"node"}}}); err != nil {
				t.Fatal(err)
			}
			if err := engine.BulkUpsertNodesDynamic([]model.NodeDynamic{{Hash: hash.Hex(), ChainProxy: tc.cached, EgressIP: "203.0.113.1", EgressRegion: "us"}}); err != nil {
				t.Fatal(err)
			}
			if err := engine.BulkUpsertNodeLatency([]model.NodeLatency{{NodeHash: hash.Hex(), Domain: "gstatic.com", ChainProxy: tc.cached, EwmaNs: int64(time.Millisecond), LastUpdatedNs: time.Now().UnixNano()}}); err != nil {
				t.Fatal(err)
			}
			runtimeCfg := config.NewDefaultRuntimeConfig()
			envCfg := newDefaultPlatformEnvConfig()
			envCfg.MaxLatencyTableEntries = 16
			subs, pool := newBootstrapTestRuntime(runtimeCfg)
			if err := bootstrapTopology(engine, subs, pool, envCfg); err != nil {
				t.Fatal(err)
			}
			builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{DNSUpstreams: []string{"local"}})
			if err != nil {
				t.Fatal(err)
			}
			defer builder.Close()
			mgr := outbound.NewOutboundManager(pool, builder)
			defer mgr.Close()
			if err := bootstrapNodes(engine, pool, subs, mgr, envCfg, runtimeCfg.LatencyAuthorities); err != nil {
				t.Fatal(err)
			}
			entry, ok := pool.GetEntry(hash)
			if !ok || !entry.HasOutbound() {
				t.Fatal("bootstrapped mode was not warmed")
			}
			if (entry.RoutingRole() == node.RoleChain) != tc.configured {
				t.Fatal("subscription role was not restored")
			}
			if tc.cached != tc.configured {
				if !entry.IsCircuitOpen() || entry.GetEgressIP().IsValid() || entry.HasLatency() {
					t.Fatal("cached measurements from another mode survived restart")
				}
			} else if entry.IsCircuitOpen() || !entry.GetEgressIP().IsValid() || !entry.HasLatency() {
				t.Fatal("matching mode cache was discarded")
			}
		})
	}
}

// A flush can drain latency marks before a concurrent mode change but read the
// new dynamic mode afterward. The next startup must reject those old samples.
func TestCrashBetweenModeMarkerAndLatencyCleanup(t *testing.T) {
	sd, cd := t.TempDir(), t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(sd, cd)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	defer closer.Close()
	must(engine.UpsertSubscription(model.Subscription{ID: "sub", Name: "sub", URL: "https://example.com", UpdateIntervalNs: int64(time.Minute), Enabled: true}))
	raw := json.RawMessage(`{"type":"http","server":"127.0.0.1","server_port":12345}`)
	hash := node.HashFromRawOptions(raw)
	must(engine.BulkUpsertNodesStatic([]model.NodeStatic{{Hash: hash.Hex(), RawOptions: raw, CreatedAtNs: time.Now().UnixNano()}}))
	must(engine.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{{SubscriptionID: "sub", NodeHash: hash.Hex()}}))
	must(engine.BulkUpsertNodesDynamic([]model.NodeDynamic{{Hash: hash.Hex()}}))
	must(engine.BulkUpsertNodeLatency([]model.NodeLatency{{NodeHash: hash.Hex(), Domain: "gstatic.com", EwmaNs: int64(7 * time.Millisecond), LastUpdatedNs: time.Now().UnixNano()}}))
	cfg := config.NewDefaultRuntimeConfig()
	env := newDefaultPlatformEnvConfig()
	env.MaxLatencyTableEntries = 16
	subs := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxLatencyTableEntries: 16, MaxConsecutiveFailures: func() int { return 3 }, OnNodeDynamicChanged: func(h node.Hash) { engine.MarkNodeDynamic(h.Hex()) }, OnNodeLatencyChanged: func(h node.Hash, d string) { engine.MarkNodeLatency(h.Hex(), d) }})
	builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{DNSUpstreams: []string{"local"}})
	must(err)
	defer builder.Close()
	mgr := outbound.NewOutboundManager(pool, builder)
	must(bootstrapTopology(engine, subs, pool, env))
	must(bootstrapNodes(engine, pool, subs, mgr, env, cfg.LatencyAuthorities))
	engine.MarkNodeStatic(hash.Hex())
	engine.MarkNodeDynamic(hash.Hex())
	readers := newFlushReaders(pool, subs, nil)
	readStatic := readers.ReadNodeStatic
	readers.ReadNodeStatic = func(key string) *model.NodeStatic {
		sub := subs.Lookup("sub")
		sub.SetChainConfig(true, false)
		must(engine.UpsertSubscription(model.Subscription{ID: "sub", Name: "sub", URL: "https://example.com", UpdateIntervalNs: int64(time.Minute), Enabled: true, ChainProxy: true}))
		pool.ReconcileNodeRole(hash)
		return readStatic(key)
	}
	must(engine.FlushDirtySets(readers))
	rows, err := engine.LoadAllNodeLatency()
	must(err)
	if len(rows) != 1 {
		t.Fatal("test must retain the pre-switch row at the crash boundary")
	}
	must(mgr.Close())
	must(closer.Close())
	// Pending in-memory marks intentionally disappear; no final flush.
	engine, closer, err = state.PersistenceBootstrap(sd, cd)
	must(err)
	defer closer.Close()
	subs2, pool2 := newBootstrapTestRuntime(cfg)
	mgr2 := outbound.NewOutboundManager(pool2, builder)
	defer mgr2.Close()
	must(bootstrapTopology(engine, subs2, pool2, env))
	must(bootstrapNodes(engine, pool2, subs2, mgr2, env, cfg.LatencyAuthorities))
	e, _ := pool2.GetEntry(hash)
	if e.RoutingRole() != node.RoleChain || e.HasLatency() {
		t.Fatal("ordinary EWMA restored into chained mode after crash")
	}
}
