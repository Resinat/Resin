package main

import (
	"encoding/json"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/metrics"
	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/outbound"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/proxy"
	"github.com/resin-proxy/resin/internal/requestlog"
	"github.com/resin-proxy/resin/internal/routing"
	"github.com/resin-proxy/resin/internal/state"
	"github.com/resin-proxy/resin/internal/testutil"
)

func TestBootstrapRestart_RecoversTopologyAndStickyLease(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	cacheDir := filepath.Join(root, "cache")

	envCfg := newDefaultPlatformEnvConfig()
	envCfg.MaxLatencyTableEntries = 16

	const (
		subID        = "sub-restart"
		subName      = "RestartSub"
		platformID   = "plat-restart"
		platformName = "RestartPlat"
		account      = "acct-restart"
		latencyHost  = "example.com"
	)

	raw := json.RawMessage(`{"type":"stub","server":"198.51.100.30","server_port":443}`)
	hash := node.HashFromRawOptions(raw)
	egressIP := netip.MustParseAddr("203.0.113.9")
	egressUpdatedAt := time.Now().UnixNano()

	engine1, closer1, err := state.PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatalf("first PersistenceBootstrap: %v", err)
	}

	now := time.Now().UnixNano()
	if err := engine1.UpsertSubscription(model.Subscription{
		ID:               subID,
		Name:             subName,
		URL:              "https://example.com/sub",
		UpdateIntervalNs: int64(30 * time.Minute),
		Enabled:          true,
		Ephemeral:        false,
		CreatedAtNs:      now,
		UpdatedAtNs:      now,
	}); err != nil {
		t.Fatalf("UpsertSubscription: %v", err)
	}
	if err := engine1.UpsertPlatform(model.Platform{
		ID:                     platformID,
		Name:                   platformName,
		StickyTTLNs:            int64(12 * time.Hour),
		RegexFilters:           []string{},
		RegionFilters:          []string{},
		ReverseProxyMissAction: "RANDOM",
		AllocationPolicy:       "BALANCED",
		UpdatedAtNs:            now,
	}); err != nil {
		t.Fatalf("UpsertPlatform: %v", err)
	}

	runtimeCfg := config.NewDefaultRuntimeConfig()
	subManager1, pool1 := newBootstrapTestRuntime(runtimeCfg)
	if err := bootstrapTopology(engine1, subManager1, pool1, envCfg); err != nil {
		t.Fatalf("first bootstrapTopology: %v", err)
	}

	sub1, ok := subManager1.Get(subID)
	if !ok {
		t.Fatalf("subscription %q not loaded on first boot", subID)
	}
	sub1.ManagedNodes().Store(hash, []string{"restart-tag"})

	pool1.AddNodeFromSub(hash, raw, subID)
	entry1, ok := pool1.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s missing after AddNodeFromSub", hash.Hex())
	}

	outboundMgr1 := outbound.NewOutboundManager(pool1, &testutil.StubOutboundBuilder{})
	outboundMgr1.EnsureNodeOutbound(hash)
	if !entry1.HasOutbound() {
		t.Fatal("node outbound should be initialized on first boot")
	}

	entry1.SetEgressIP(egressIP)
	entry1.LastEgressUpdate.Store(egressUpdatedAt)
	entry1.FailureCount.Store(2)
	if entry1.LatencyTable == nil {
		t.Fatal("node latency table should be initialized")
	}
	entry1.LatencyTable.Update(latencyHost, 60*time.Millisecond, 5*time.Minute)
	pool1.NotifyNodeDirty(hash)

	plat1, ok := pool1.GetPlatform(platformID)
	if !ok {
		t.Fatalf("platform %q missing after bootstrap", platformID)
	}
	if !plat1.View().Contains(hash) {
		t.Fatal("node should be routable before first shutdown")
	}

	router1 := routing.NewRouter(routing.RouterConfig{
		Pool:            pool1,
		Authorities:     func() []string { return []string{latencyHost} },
		P2CWindow:       func() time.Duration { return 5 * time.Minute },
		NodeTagResolver: pool1.ResolveNodeDisplayTag,
	})

	firstRoute, err := router1.RouteRequest(platformName, account, "https://example.com/restart")
	if err != nil {
		t.Fatalf("first RouteRequest: %v", err)
	}
	if !firstRoute.LeaseCreated {
		t.Fatal("first sticky route should create lease")
	}
	if firstRoute.NodeHash != hash {
		t.Fatalf("first route hash: got %s, want %s", firstRoute.NodeHash.Hex(), hash.Hex())
	}
	if firstRoute.NodeTag != subName+"/restart-tag" {
		t.Fatalf("first route node tag: got %q, want %q", firstRoute.NodeTag, subName+"/restart-tag")
	}
	firstLease := router1.ReadLease(model.LeaseKey{PlatformID: platformID, Account: account})
	if firstLease == nil || firstLease.CreatedAtNs <= 0 {
		t.Fatalf("first lease created_at_ns should be > 0, got %+v", firstLease)
	}

	engine1.MarkNodeStatic(hash.Hex())
	engine1.MarkSubscriptionNode(subID, hash.Hex())
	engine1.MarkNodeDynamic(hash.Hex())
	engine1.MarkNodeLatency(hash.Hex(), latencyHost)
	engine1.MarkLease(platformID, account)
	if err := engine1.FlushDirtySets(newFlushReaders(pool1, subManager1, router1)); err != nil {
		t.Fatalf("first FlushDirtySets: %v", err)
	}

	if err := closer1.Close(); err != nil {
		t.Fatalf("first closer.Close: %v", err)
	}

	engine2, closer2, err := state.PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatalf("second PersistenceBootstrap: %v", err)
	}
	defer func() { _ = closer2.Close() }()

	subManager2, pool2 := newBootstrapTestRuntime(runtimeCfg)
	if err := bootstrapTopology(engine2, subManager2, pool2, envCfg); err != nil {
		t.Fatalf("second bootstrapTopology: %v", err)
	}

	outboundMgr2 := outbound.NewOutboundManager(pool2, &testutil.StubOutboundBuilder{})
	if err := bootstrapNodes(engine2, pool2, subManager2, outboundMgr2, envCfg); err != nil {
		t.Fatalf("bootstrapNodes: %v", err)
	}
	pool2.RebuildAllPlatforms()

	router2 := routing.NewRouter(routing.RouterConfig{
		Pool:            pool2,
		Authorities:     func() []string { return []string{latencyHost} },
		P2CWindow:       func() time.Duration { return 5 * time.Minute },
		NodeTagResolver: pool2.ResolveNodeDisplayTag,
	})
	leases, err := engine2.LoadAllLeases()
	if err != nil {
		t.Fatalf("LoadAllLeases: %v", err)
	}
	if len(leases) != 1 {
		t.Fatalf("expected 1 persisted lease, got %d", len(leases))
	}
	if leases[0].CreatedAtNs != firstLease.CreatedAtNs {
		t.Fatalf("persisted lease created_at_ns: got %d, want %d", leases[0].CreatedAtNs, firstLease.CreatedAtNs)
	}
	router2.RestoreLeases(leases)
	restoredLease := router2.ReadLease(model.LeaseKey{PlatformID: platformID, Account: account})
	if restoredLease == nil || restoredLease.CreatedAtNs != firstLease.CreatedAtNs {
		t.Fatalf("restored lease created_at_ns: got %+v, want %d", restoredLease, firstLease.CreatedAtNs)
	}

	if _, ok := pool2.GetPlatform(platform.DefaultPlatformID); !ok {
		t.Fatal("default platform should exist after restart")
	}
	if _, ok := pool2.GetPlatform(platformID); !ok {
		t.Fatalf("platform %q should exist after restart", platformID)
	}

	sub2, ok := subManager2.Get(subID)
	if !ok {
		t.Fatalf("subscription %q missing after restart", subID)
	}
	tags, ok := sub2.ManagedNodes().Load(hash)
	if !ok {
		t.Fatalf("subscription node binding for %s missing after restart", hash.Hex())
	}
	if !reflect.DeepEqual(tags, []string{"restart-tag"}) {
		t.Fatalf("restored tags: got %v, want %v", tags, []string{"restart-tag"})
	}

	entry2, ok := pool2.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s missing after restart", hash.Hex())
	}
	if got := entry2.FailureCount.Load(); got != 2 {
		t.Fatalf("failure_count: got %d, want %d", got, 2)
	}
	if got := entry2.GetEgressIP(); got != egressIP {
		t.Fatalf("egress_ip: got %s, want %s", got, egressIP)
	}
	if got := entry2.LastEgressUpdate.Load(); got != egressUpdatedAt {
		t.Fatalf("egress_updated_at_ns: got %d, want %d", got, egressUpdatedAt)
	}
	if entry2.LatencyTable == nil {
		t.Fatal("latency table should be restored")
	}
	stats, ok := entry2.LatencyTable.GetDomainStats(latencyHost)
	if !ok {
		t.Fatalf("latency domain %q missing after restart", latencyHost)
	}
	if stats.Ewma != 60*time.Millisecond {
		t.Fatalf("latency ewma: got %v, want %v", stats.Ewma, 60*time.Millisecond)
	}

	subIDs := entry2.SubscriptionIDs()
	if len(subIDs) != 1 || subIDs[0] != subID {
		t.Fatalf("restored subscription ids: got %v, want [%s]", subIDs, subID)
	}

	plat2, _ := pool2.GetPlatform(platformID)
	if !plat2.View().Contains(hash) {
		t.Fatal("node should be routable after restart rebuild")
	}

	secondRoute, err := router2.RouteRequest(platformName, account, "https://example.com/restart")
	if err != nil {
		t.Fatalf("second RouteRequest: %v", err)
	}
	if secondRoute.NodeHash != hash {
		t.Fatalf("restored route hash: got %s, want %s", secondRoute.NodeHash.Hex(), hash.Hex())
	}
	if secondRoute.LeaseCreated {
		t.Fatal("restored sticky lease should be reused, not recreated")
	}
	if secondRoute.NodeTag != subName+"/restart-tag" {
		t.Fatalf("restored route node tag: got %q, want %q", secondRoute.NodeTag, subName+"/restart-tag")
	}
}

func TestBootstrapRestart_RecoversObservabilityPersistence(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	metricsDBPath := filepath.Join(logDir, "metrics.db")

	const platformID = "plat-observability"

	// First run: produce request logs and metric events, then graceful stop.
	reqRepo1 := requestlog.NewRepo(logDir, 64*1024*1024, 5)
	if err := reqRepo1.Open(); err != nil {
		t.Fatalf("reqRepo1.Open: %v", err)
	}
	reqSvc1 := requestlog.NewService(requestlog.ServiceConfig{
		Repo:          reqRepo1,
		QueueSize:     8,
		FlushBatch:    16,
		FlushInterval: time.Hour,
	})
	reqSvc1.Start()
	reqSvc1.EmitRequestLog(proxy.RequestLogEntry{
		StartedAtNs:  time.Now().UnixNano(),
		ProxyType:    proxy.ProxyTypeForward,
		ClientIP:     "127.0.0.1",
		PlatformID:   platformID,
		PlatformName: "Observability",
		Account:      "acct-observability",
		TargetHost:   "example.com",
		TargetURL:    "https://example.com/obs",
		DurationNs:   int64(25 * time.Millisecond),
		NetOK:        true,
		HTTPMethod:   "GET",
		HTTPStatus:   200,
	})
	reqSvc1.Stop() // flush queued entries on graceful stop
	if err := reqRepo1.Close(); err != nil {
		t.Fatalf("reqRepo1.Close: %v", err)
	}

	metricsRepo1, err := metrics.NewMetricsRepo(metricsDBPath)
	if err != nil {
		t.Fatalf("metrics.NewMetricsRepo(first): %v", err)
	}
	metricsMgr1 := metrics.NewManager(metrics.ManagerConfig{
		Repo:                        metricsRepo1,
		LatencyBinMs:                100,
		LatencyOverflowMs:           3000,
		BucketSeconds:               300,
		ThroughputRealtimeCapacity:  16,
		ThroughputIntervalSec:       1,
		ConnectionsRealtimeCapacity: 16,
		ConnectionsIntervalSec:      5,
		LeasesRealtimeCapacity:      16,
		LeasesIntervalSec:           5,
	})
	metricsMgr1.OnTrafficDelta(platformID, 100, 200)
	metricsMgr1.OnRequestFinished(proxy.RequestFinishedEvent{
		PlatformID: platformID,
		ProxyType:  proxy.ProxyTypeForward,
		NetOK:      true,
		DurationNs: int64(120 * time.Millisecond),
	})
	metricsMgr1.OnRequestFinished(proxy.RequestFinishedEvent{
		PlatformID: platformID,
		ProxyType:  proxy.ProxyTypeForward,
		NetOK:      false,
		DurationNs: int64(240 * time.Millisecond),
	})
	metricsMgr1.OnProbeEvent(metrics.ProbeEvent{Kind: metrics.ProbeKindEgress})
	metricsMgr1.OnLeaseEvent(metrics.LeaseMetricEvent{
		PlatformID: platformID,
		Op:         metrics.LeaseOpRemove,
		LifetimeNs: int64(30 * time.Second),
	})
	metricsMgr1.ThroughputRing().Push(metrics.RealtimeSample{
		Timestamp:  time.Now(),
		IngressBPS: 123,
		EgressBPS:  456,
	})
	metricsMgr1.Stop() // graceful stop flushes current in-memory bucket
	if err := metricsRepo1.Close(); err != nil {
		t.Fatalf("metricsRepo1.Close: %v", err)
	}

	// Restart: reopen repos and validate persistence + recovery semantics.
	reqRepo2 := requestlog.NewRepo(logDir, 64*1024*1024, 5)
	if err := reqRepo2.Open(); err != nil {
		t.Fatalf("reqRepo2.Open: %v", err)
	}
	defer reqRepo2.Close()

	rows, hasMore, nextCursor, err := reqRepo2.List(requestlog.ListFilter{
		PlatformID: platformID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("reqRepo2.List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("request log rows: got %d, want %d", len(rows), 1)
	}
	if hasMore {
		t.Fatalf("request log hasMore: got %v, want false", hasMore)
	}
	if nextCursor != nil {
		t.Fatalf("request log nextCursor: got %+v, want nil", nextCursor)
	}
	if rows[0].TargetURL != "https://example.com/obs" {
		t.Fatalf("request log target_url: got %q, want %q", rows[0].TargetURL, "https://example.com/obs")
	}

	metricsRepo2, err := metrics.NewMetricsRepo(metricsDBPath)
	if err != nil {
		t.Fatalf("metrics.NewMetricsRepo(restart): %v", err)
	}
	defer metricsRepo2.Close()

	from, to := int64(0), time.Now().Add(time.Hour).Unix()
	trafficRows, err := metricsRepo2.QueryTraffic(from, to, platformID)
	if err != nil {
		t.Fatalf("QueryTraffic: %v", err)
	}
	if len(trafficRows) != 1 || trafficRows[0].IngressBytes != 100 || trafficRows[0].EgressBytes != 200 {
		t.Fatalf("traffic rows: got %+v", trafficRows)
	}

	requestRows, err := metricsRepo2.QueryRequests(from, to, platformID)
	if err != nil {
		t.Fatalf("QueryRequests: %v", err)
	}
	if len(requestRows) != 1 || requestRows[0].TotalRequests != 2 || requestRows[0].SuccessRequests != 1 {
		t.Fatalf("request rows: got %+v", requestRows)
	}

	probeRows, err := metricsRepo2.QueryProbes(from, to)
	if err != nil {
		t.Fatalf("QueryProbes: %v", err)
	}
	if len(probeRows) != 1 || probeRows[0].TotalCount != 1 {
		t.Fatalf("probe rows: got %+v", probeRows)
	}

	leaseRows, err := metricsRepo2.QueryLeaseLifetime(from, to, platformID)
	if err != nil {
		t.Fatalf("QueryLeaseLifetime: %v", err)
	}
	if len(leaseRows) != 1 || leaseRows[0].SampleCount != 1 {
		t.Fatalf("lease lifetime rows: got %+v", leaseRows)
	}

	metricsMgr2 := metrics.NewManager(metrics.ManagerConfig{
		Repo:                        metricsRepo2,
		LatencyBinMs:                100,
		LatencyOverflowMs:           3000,
		BucketSeconds:               300,
		ThroughputRealtimeCapacity:  16,
		ThroughputIntervalSec:       1,
		ConnectionsRealtimeCapacity: 16,
		ConnectionsIntervalSec:      5,
		LeasesRealtimeCapacity:      16,
		LeasesIntervalSec:           5,
	})
	if _, ok := metricsMgr2.ThroughputRing().Latest(); ok {
		t.Fatal("realtime throughput ring should not be recovered after restart")
	}
	if _, ok := metricsMgr2.ConnectionsRing().Latest(); ok {
		t.Fatal("realtime connections ring should not be recovered after restart")
	}
	if _, ok := metricsMgr2.LeasesRing().Latest(); ok {
		t.Fatal("realtime leases ring should not be recovered after restart")
	}
}
