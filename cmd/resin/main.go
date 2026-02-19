package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/netip"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"

	"github.com/resin-proxy/resin/internal/buildinfo"
	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/geoip"
	"github.com/resin-proxy/resin/internal/metrics"
	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/netutil"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/outbound"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/probe"
	"github.com/resin-proxy/resin/internal/proxy"
	"github.com/resin-proxy/resin/internal/requestlog"
	"github.com/resin-proxy/resin/internal/routing"
	"github.com/resin-proxy/resin/internal/state"
	"github.com/resin-proxy/resin/internal/subscription"
	"github.com/resin-proxy/resin/internal/topology"
)

type topologyRuntime struct {
	subManager       *topology.SubscriptionManager
	pool             *topology.GlobalNodePool
	probeMgr         *probe.ProbeManager
	scheduler        *topology.SubscriptionScheduler
	ephemeralCleaner *topology.EphemeralCleaner
	router           *routing.Router
	leaseCleaner     *routing.LeaseCleaner
	outboundMgr      *outbound.OutboundManager
	singboxBuilder   *outbound.SingboxBuilder // for Close on shutdown
}

func main() {
	if err := run(); err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fatal: "+format+"\n", args...)
	os.Exit(1)
}

func loadRuntimeConfig(engine *state.StateEngine) *config.RuntimeConfig {
	runtimeCfg, ver, err := engine.GetSystemConfig()
	if err != nil {
		fatalf("load system config: %v", err)
	}
	if runtimeCfg == nil {
		log.Println("No persisted runtime config found, using defaults")
		return config.NewDefaultRuntimeConfig()
	}
	log.Printf("Loaded persisted runtime config (version %d)", ver)
	return runtimeCfg
}

func newDirectDownloader(
	envCfg *config.EnvConfig,
	runtimeCfg *atomic.Pointer[config.RuntimeConfig],
) *netutil.DirectDownloader {
	return netutil.NewDirectDownloader(
		func() time.Duration {
			return envCfg.ResourceFetchTimeout
		},
		func() string {
			return currentDownloadUserAgent(runtimeCfg)
		},
	)
}

func currentDownloadUserAgent(runtimeCfg *atomic.Pointer[config.RuntimeConfig]) string {
	ua := runtimeConfigSnapshot(runtimeCfg).UserAgent
	if ua == "" {
		ua = "Resin/" + buildinfo.Version
	}
	return ua
}

func runtimeConfigSnapshot(runtimeCfg *atomic.Pointer[config.RuntimeConfig]) *config.RuntimeConfig {
	if runtimeCfg == nil {
		return config.NewDefaultRuntimeConfig()
	}
	cfg := runtimeCfg.Load()
	if cfg == nil {
		return config.NewDefaultRuntimeConfig()
	}
	return cfg
}

type requestLogRuntimeSettings struct {
	DBMaxBytes    int64
	DBRetainCount int
	QueueSize     int
	FlushBatch    int
	FlushInterval time.Duration
}

func deriveRequestLogRuntimeSettings(envCfg *config.EnvConfig) requestLogRuntimeSettings {
	return requestLogRuntimeSettings{
		DBMaxBytes:    int64(envCfg.RequestLogDBMaxMB) * 1024 * 1024,
		DBRetainCount: envCfg.RequestLogDBRetainCount,
		QueueSize:     envCfg.RequestLogQueueSize,
		FlushBatch:    envCfg.RequestLogQueueFlushBatchSize,
		FlushInterval: envCfg.RequestLogQueueFlushInterval,
	}
}

type metricsManagerSettings struct {
	LatencyBinMs                int
	LatencyOverflowMs           int
	BucketSeconds               int
	ThroughputIntervalSec       int
	ThroughputRealtimeCapacity  int
	ConnectionsIntervalSec      int
	ConnectionsRealtimeCapacity int
	LeasesIntervalSec           int
	LeasesRealtimeCapacity      int
}

func deriveMetricsManagerSettings(envCfg *config.EnvConfig) metricsManagerSettings {
	return metricsManagerSettings{
		LatencyBinMs:                envCfg.MetricLatencyBinWidthMS,
		LatencyOverflowMs:           envCfg.MetricLatencyBinOverflowMS,
		BucketSeconds:               envCfg.MetricBucketSeconds,
		ThroughputIntervalSec:       envCfg.MetricThroughputIntervalSeconds,
		ThroughputRealtimeCapacity:  realtimeCapacity(envCfg.MetricThroughputRetentionSeconds, envCfg.MetricThroughputIntervalSeconds),
		ConnectionsIntervalSec:      envCfg.MetricConnectionsIntervalSeconds,
		ConnectionsRealtimeCapacity: realtimeCapacity(envCfg.MetricConnectionsRetentionSeconds, envCfg.MetricConnectionsIntervalSeconds),
		LeasesIntervalSec:           envCfg.MetricLeasesIntervalSeconds,
		LeasesRealtimeCapacity:      realtimeCapacity(envCfg.MetricLeasesRetentionSeconds, envCfg.MetricLeasesIntervalSeconds),
	}
}

func realtimeCapacity(retentionSec, intervalSec int) int {
	if intervalSec <= 0 {
		intervalSec = 1
	}
	if retentionSec <= 0 {
		retentionSec = intervalSec
	}
	capacity := retentionSec / intervalSec
	if retentionSec%intervalSec != 0 {
		capacity++
	}
	if capacity <= 0 {
		capacity = 1
	}
	return capacity
}

func newGeoIPService(
	cacheDir string,
	updateSchedule string,
	downloader netutil.Downloader,
) *geoip.Service {
	geoSvc := geoip.NewService(geoip.ServiceConfig{
		CacheDir:       cacheDir,
		UpdateSchedule: updateSchedule,
		Downloader:     downloader,
		OpenDB:         geoip.SingBoxOpen,
	})
	return geoSvc
}

func startGeoIPService(geoSvc *geoip.Service) {
	if err := geoSvc.Start(); err != nil {
		log.Printf("GeoIP service start (non-fatal): %v", err)
	}
	log.Println("GeoIP service initialized")
}

func newTopologyRuntime(
	engine *state.StateEngine,
	envCfg *config.EnvConfig,
	runtimeCfg *atomic.Pointer[config.RuntimeConfig],
	geoSvc *geoip.Service,
	downloader netutil.Downloader,
	onProbeConnLifecycle func(netutil.ConnLifecycleOp),
) (*topologyRuntime, error) {
	subManager := topology.NewSubscriptionManager()

	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup: subManager.Lookup,
		GeoLookup: geoSvc.Lookup,
		OnSubNodeChanged: func(subID string, hash node.Hash, added bool) {
			if added {
				engine.MarkSubscriptionNode(subID, hash.Hex())
			} else {
				engine.MarkSubscriptionNodeDelete(subID, hash.Hex())
			}
		},
		OnNodeDynamicChanged: func(hash node.Hash) {
			engine.MarkNodeDynamic(hash.Hex())
		},
		OnNodeLatencyChanged: func(hash node.Hash, domain string) {
			engine.MarkNodeLatency(hash.Hex(), domain)
		},
		MaxLatencyTableEntries: envCfg.MaxLatencyTableEntries,
		MaxConsecutiveFailures: func() int {
			return runtimeConfigSnapshot(runtimeCfg).MaxConsecutiveFailures
		},
		LatencyDecayWindow: func() time.Duration {
			return time.Duration(runtimeConfigSnapshot(runtimeCfg).LatencyDecayWindow)
		},
	})
	log.Println("Topology: GlobalNodePool initialized")

	singboxBuilder, err := outbound.NewSingboxBuilder()
	if err != nil {
		return nil, fmt.Errorf("singbox builder: %w", err)
	}
	outboundMgr := outbound.NewOutboundManager(pool, singboxBuilder)

	probeMgr := probe.NewProbeManager(probe.ProbeConfig{
		Pool:        pool,
		Concurrency: envCfg.ProbeConcurrency,
		Fetcher: func(hash node.Hash, url string) ([]byte, time.Duration, error) {
			ctx, cancel := context.WithTimeout(context.Background(), envCfg.ProbeTimeout)
			defer cancel()
			entry, ok := pool.GetEntry(hash)
			if !ok {
				return nil, 0, fmt.Errorf("node not found")
			}
			outboundPtr := entry.Outbound.Load()
			if outboundPtr == nil {
				return nil, 0, outbound.ErrOutboundNotReady
			}
			return netutil.HTTPGetViaOutbound(ctx, *outboundPtr, url, netutil.OutboundHTTPOptions{
				RequireStatusOK: false,
				OnConnLifecycle: func(op netutil.ConnLifecycleOp) {
					if onProbeConnLifecycle != nil {
						onProbeConnLifecycle(op)
					}
				},
			})
		},
		MaxEgressTestInterval: func() time.Duration {
			return time.Duration(runtimeConfigSnapshot(runtimeCfg).MaxEgressTestInterval)
		},
		MaxLatencyTestInterval: func() time.Duration {
			return time.Duration(runtimeConfigSnapshot(runtimeCfg).MaxLatencyTestInterval)
		},
		MaxAuthorityLatencyTestInterval: func() time.Duration {
			return time.Duration(runtimeConfigSnapshot(runtimeCfg).MaxAuthorityLatencyTestInterval)
		},
		LatencyTestURL: func() string {
			return runtimeConfigSnapshot(runtimeCfg).LatencyTestURL
		},
		LatencyAuthorities: func() []string {
			return runtimeConfigSnapshot(runtimeCfg).LatencyAuthorities
		},
	})

	pool.SetOnNodeAdded(func(hash node.Hash) {
		engine.MarkNodeStatic(hash.Hex())
		outboundMgr.EnsureNodeOutbound(hash)
		// No NotifyNodeDirty here — AddNodeFromSub already notifies all platforms.
		probeMgr.TriggerImmediateEgressProbe(hash)
	})
	pool.SetOnNodeRemoved(func(hash node.Hash, entry *node.NodeEntry) {
		markNodeRemovedDirty(engine, hash, entry)
		outboundMgr.RemoveNodeOutbound(entry)
	})
	log.Println("ProbeManager initialized")

	scheduler := topology.NewSubscriptionScheduler(topology.SchedulerConfig{
		SubManager: subManager,
		Pool:       pool,
		Downloader: downloader,
	})
	ephemeralCleaner := topology.NewEphemeralCleaner(
		subManager,
		pool,
		func() time.Duration {
			return time.Duration(runtimeConfigSnapshot(runtimeCfg).EphemeralNodeEvictDelay)
		},
	)

	return &topologyRuntime{
		subManager:       subManager,
		pool:             pool,
		probeMgr:         probeMgr,
		scheduler:        scheduler,
		ephemeralCleaner: ephemeralCleaner,
		outboundMgr:      outboundMgr,
		singboxBuilder:   singboxBuilder,
	}, nil
}

func markNodeRemovedDirty(engine *state.StateEngine, hash node.Hash, entry *node.NodeEntry) {
	hashHex := hash.Hex()
	engine.MarkNodeStaticDelete(hashHex)
	engine.MarkNodeDynamicDelete(hashHex)

	if entry == nil || entry.LatencyTable == nil {
		return
	}
	entry.LatencyTable.Range(func(domain string, _ node.DomainLatencyStats) bool {
		engine.MarkNodeLatencyDelete(hashHex, domain)
		return true
	})
}

func bootstrapTopology(
	engine *state.StateEngine,
	subManager *topology.SubscriptionManager,
	pool *topology.GlobalNodePool,
	envCfg *config.EnvConfig,
) error {
	dbSubs, err := engine.ListSubscriptions()
	if err != nil {
		return fmt.Errorf("load subscriptions: %w", err)
	}
	for _, ms := range dbSubs {
		sub := subscription.NewSubscription(ms.ID, ms.Name, ms.URL, ms.Enabled, ms.Ephemeral)
		sub.SetFetchConfig(ms.URL, ms.UpdateIntervalNs)
		sub.CreatedAtNs = ms.CreatedAtNs
		sub.UpdatedAtNs = ms.UpdatedAtNs
		subManager.Register(sub)
	}
	log.Printf("Loaded %d subscriptions from state.db", len(dbSubs))

	dbPlats, err := engine.ListPlatforms()
	if err != nil {
		return fmt.Errorf("load platforms: %w", err)
	}
	if err := ensureDefaultPlatform(engine, envCfg, dbPlats); err != nil {
		return fmt.Errorf("ensure default platform: %w", err)
	}
	dbPlats, err = engine.ListPlatforms()
	if err != nil {
		return fmt.Errorf("reload platforms: %w", err)
	}
	for _, mp := range dbPlats {
		plat, err := platform.BuildFromModel(mp)
		if err != nil {
			return err
		}
		pool.RegisterPlatform(plat)
	}
	log.Printf("Loaded %d platforms from state.db", len(dbPlats))
	return nil
}

func ensureDefaultPlatform(
	engine *state.StateEngine,
	envCfg *config.EnvConfig,
	platformsInDB []model.Platform,
) error {
	hasDefaultID := false
	for _, p := range platformsInDB {
		if p.ID == platform.DefaultPlatformID {
			hasDefaultID = true
		}
	}
	if hasDefaultID {
		return nil
	}

	defaultPlatform := model.Platform{
		ID:                     platform.DefaultPlatformID,
		Name:                   platform.DefaultPlatformName,
		StickyTTLNs:            int64(envCfg.DefaultPlatformStickyTTL),
		RegexFilters:           append([]string(nil), envCfg.DefaultPlatformRegexFilters...),
		RegionFilters:          append([]string(nil), envCfg.DefaultPlatformRegionFilters...),
		ReverseProxyMissAction: envCfg.DefaultPlatformReverseProxyMissAction,
		AllocationPolicy:       envCfg.DefaultPlatformAllocationPolicy,
		UpdatedAtNs:            time.Now().UnixNano(),
	}
	if err := engine.UpsertPlatform(defaultPlatform); err != nil {
		return err
	}
	log.Println("Created built-in Default platform")
	return nil
}

func newFlushReaders(
	pool *topology.GlobalNodePool,
	subManager *topology.SubscriptionManager,
	router *routing.Router,
) state.CacheReaders {
	return state.CacheReaders{
		ReadNodeStatic: func(hash string) *model.NodeStatic {
			h, err := node.ParseHex(hash)
			if err != nil {
				return nil
			}
			entry, ok := pool.GetEntry(h)
			if !ok {
				return nil
			}
			return &model.NodeStatic{
				Hash:        hash,
				RawOptions:  append(json.RawMessage(nil), entry.RawOptions...),
				CreatedAtNs: entry.CreatedAt.UnixNano(),
			}
		},
		ReadNodeDynamic: func(hash string) *model.NodeDynamic {
			h, err := node.ParseHex(hash)
			if err != nil {
				return nil
			}
			entry, ok := pool.GetEntry(h)
			if !ok {
				return nil
			}
			egressIP := entry.GetEgressIP()
			egressStr := ""
			if egressIP.IsValid() {
				egressStr = egressIP.String()
			}
			return &model.NodeDynamic{
				Hash:              hash,
				FailureCount:      int(entry.FailureCount.Load()),
				CircuitOpenSince:  entry.CircuitOpenSince.Load(),
				EgressIP:          egressStr,
				EgressUpdatedAtNs: entry.LastEgressUpdate.Load(),
			}
		},
		ReadNodeLatency: func(key model.NodeLatencyKey) *model.NodeLatency {
			h, err := node.ParseHex(key.NodeHash)
			if err != nil {
				return nil
			}
			entry, ok := pool.GetEntry(h)
			if !ok || entry.LatencyTable == nil {
				return nil
			}
			stats, ok := entry.LatencyTable.GetDomainStats(key.Domain)
			if !ok {
				return nil
			}
			return &model.NodeLatency{
				NodeHash:      key.NodeHash,
				Domain:        key.Domain,
				EwmaNs:        int64(stats.Ewma),
				LastUpdatedNs: stats.LastUpdated.UnixNano(),
			}
		},
		ReadLease: func(key model.LeaseKey) *model.Lease {
			return router.ReadLease(key)
		},
		ReadSubscriptionNode: func(key model.SubscriptionNodeKey) *model.SubscriptionNode {
			h, err := node.ParseHex(key.NodeHash)
			if err != nil {
				return nil
			}
			sub := subManager.Lookup(key.SubscriptionID)
			if sub == nil {
				return nil
			}
			tags, ok := sub.ManagedNodes().Load(h)
			if !ok {
				return nil
			}
			return &model.SubscriptionNode{
				SubscriptionID: key.SubscriptionID,
				NodeHash:       key.NodeHash,
				Tags:           append([]string(nil), tags...),
			}
		},
	}
}

func buildAccountMatcher(engine *state.StateEngine) *proxy.AccountMatcherRuntime {
	rules, err := engine.ListAccountHeaderRules()
	if err != nil {
		log.Printf("Warning: load account header rules: %v", err)
		return proxy.NewAccountMatcherRuntime(proxy.BuildAccountMatcher(nil))
	}
	if len(rules) > 0 {
		log.Printf("Loaded %d account header rules", len(rules))
	}
	return proxy.NewAccountMatcherRuntime(proxy.BuildAccountMatcher(rules))
}

// --- Metrics runtime stats adapter ---

// runtimeStatsAdapter implements metrics.RuntimeStatsProvider using
// GlobalNodePool + Router.
type runtimeStatsAdapter struct {
	pool        *topology.GlobalNodePool
	router      *routing.Router
	authorities func() []string
}

func (a *runtimeStatsAdapter) TotalNodes() int { return a.pool.Size() }

func (a *runtimeStatsAdapter) HealthyNodes() int {
	count := 0
	a.pool.RangeNodes(func(_ node.Hash, entry *node.NodeEntry) bool {
		if !entry.IsCircuitOpen() && entry.HasOutbound() {
			count++
		}
		return true
	})
	return count
}

func (a *runtimeStatsAdapter) EgressIPCount() int {
	seen := make(map[netip.Addr]struct{})
	a.pool.RangeNodes(func(_ node.Hash, entry *node.NodeEntry) bool {
		if ip := entry.GetEgressIP(); ip.IsValid() {
			seen[ip] = struct{}{}
		}
		return true
	})
	return len(seen)
}

func (a *runtimeStatsAdapter) LeaseCountsByPlatform() map[string]int {
	result := make(map[string]int)
	a.pool.RangePlatforms(func(plat *platform.Platform) bool {
		count := 0
		a.router.RangeLeases(plat.ID, func(_ string, _ routing.Lease) bool {
			count++
			return true
		})
		if count > 0 {
			result[plat.ID] = count
		}
		return true
	})
	return result
}

func (a *runtimeStatsAdapter) RoutableNodeCount(platformID string) (int, bool) {
	plat, ok := a.pool.GetPlatform(platformID)
	if !ok {
		return 0, false
	}
	return plat.View().Size(), true
}

func (a *runtimeStatsAdapter) PlatformEgressIPCount(platformID string) (int, bool) {
	plat, ok := a.pool.GetPlatform(platformID)
	if !ok {
		return 0, false
	}
	seen := make(map[netip.Addr]struct{})
	plat.View().Range(func(h node.Hash) bool {
		entry, ok := a.pool.GetEntry(h)
		if ok {
			if ip := entry.GetEgressIP(); ip.IsValid() {
				seen[ip] = struct{}{}
			}
		}
		return true
	})
	return len(seen), true
}

func (a *runtimeStatsAdapter) CollectNodeEWMAs(platformID string) []float64 {
	authorities := a.authorities()
	var ewmas []float64

	if platformID == "" {
		// Global: iterate all nodes.
		a.pool.RangeNodes(func(_ node.Hash, entry *node.NodeEntry) bool {
			if avg, ok := nodeAvgEWMA(entry, authorities); ok {
				ewmas = append(ewmas, avg)
			}
			return true
		})
	} else {
		// Platform-scoped: iterate only nodes routable by this platform.
		plat, ok := a.pool.GetPlatform(platformID)
		if !ok {
			return nil
		}
		plat.View().Range(func(h node.Hash) bool {
			entry, ok := a.pool.GetEntry(h)
			if ok {
				if avg, ok := nodeAvgEWMA(entry, authorities); ok {
					ewmas = append(ewmas, avg)
				}
			}
			return true
		})
	}
	return ewmas
}

// nodeAvgEWMA computes the average EWMA across authority domains for a node.
func nodeAvgEWMA(entry *node.NodeEntry, authorities []string) (float64, bool) {
	if entry.LatencyTable == nil || entry.LatencyTable.Size() == 0 {
		return 0, false
	}
	var sumMs float64
	var count int
	for _, domain := range authorities {
		if stats, ok := entry.LatencyTable.GetDomainStats(domain); ok {
			sumMs += float64(stats.Ewma.Milliseconds())
			count++
		}
	}
	if count == 0 {
		return 0, false
	}
	return sumMs / float64(count), true
}

// compositeEmitter dispatches proxy events to both requestlog and metrics.
type compositeEmitter struct {
	logSvc     *requestlog.Service
	metricsMgr *metrics.Manager
}

func (c compositeEmitter) EmitRequestFinished(ev proxy.RequestFinishedEvent) {
	c.metricsMgr.OnRequestFinished(ev)
}

func (c compositeEmitter) EmitRequestLog(ev proxy.RequestLogEntry) {
	c.logSvc.EmitRequestLog(ev)
}

func loadBootstrapNodeStatics(
	engine *state.StateEngine,
	pool *topology.GlobalNodePool,
	envCfg *config.EnvConfig,
) ([]node.Hash, error) {
	statics, err := engine.LoadAllNodesStatic()
	if err != nil {
		return nil, fmt.Errorf("load nodes_static: %w", err)
	}

	hashes := make([]node.Hash, 0, len(statics))
	for _, ns := range statics {
		hash, err := node.ParseHex(ns.Hash)
		if err != nil {
			log.Printf("[bootstrap] skip node %s: %v", ns.Hash, err)
			continue
		}
		entry := &node.NodeEntry{
			Hash:       hash,
			RawOptions: append(json.RawMessage(nil), ns.RawOptions...),
			CreatedAt:  time.Unix(0, ns.CreatedAtNs),
		}
		entry.LatencyTable = node.NewLatencyTable(envCfg.MaxLatencyTableEntries)
		pool.LoadNodeFromBootstrap(entry)
		hashes = append(hashes, hash)
	}
	log.Printf("Loaded %d static nodes from cache.db", len(statics))
	return hashes, nil
}

func warmupBootstrapOutbounds(
	hashes []node.Hash,
	outboundMgr *outbound.OutboundManager,
) {
	if len(hashes) == 0 {
		return
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	hashCh := make(chan node.Hash, len(hashes))
	for _, h := range hashes {
		hashCh <- h
	}
	close(hashCh)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for h := range hashCh {
				outboundMgr.EnsureNodeOutbound(h)
			}
		}()
	}
	wg.Wait()
	log.Printf("Parallel outbound init complete (%d workers)", workers)
}

func restoreBootstrapSubscriptionBindings(
	engine *state.StateEngine,
	pool *topology.GlobalNodePool,
	subManager *topology.SubscriptionManager,
) error {
	subNodes, err := engine.LoadAllSubscriptionNodes()
	if err != nil {
		return fmt.Errorf("load subscription_nodes: %w", err)
	}

	// Group by subscription ID for batch processing.
	subNodeMap := make(map[string][]model.SubscriptionNode)
	for _, sn := range subNodes {
		subNodeMap[sn.SubscriptionID] = append(subNodeMap[sn.SubscriptionID], sn)
	}
	for subID, nodes := range subNodeMap {
		sub, ok := subManager.Get(subID)
		if !ok {
			log.Printf("[bootstrap] subscription %s not found, skipping %d node bindings", subID, len(nodes))
			continue
		}
		managed := xsync.NewMap[node.Hash, []string]()
		for _, sn := range nodes {
			hash, err := node.ParseHex(sn.NodeHash)
			if err != nil {
				continue
			}
			managed.Store(hash, append([]string(nil), sn.Tags...))
			// Also set the node's subscription ID reference.
			if entry, ok := pool.GetEntry(hash); ok {
				entry.AddSubscriptionID(subID)
			}
		}
		sub.SwapManagedNodes(managed)
	}
	log.Printf("Loaded %d subscription-node bindings from cache.db", len(subNodes))
	return nil
}

func restoreBootstrapNodeDynamics(
	engine *state.StateEngine,
	pool *topology.GlobalNodePool,
) error {
	dynamics, err := engine.LoadAllNodesDynamic()
	if err != nil {
		return fmt.Errorf("load nodes_dynamic: %w", err)
	}

	for _, nd := range dynamics {
		hash, err := node.ParseHex(nd.Hash)
		if err != nil {
			continue
		}
		entry, ok := pool.GetEntry(hash)
		if !ok {
			continue
		}
		entry.FailureCount.Store(int32(nd.FailureCount))
		entry.CircuitOpenSince.Store(nd.CircuitOpenSince)
		if nd.EgressIP != "" {
			if ip, err := netip.ParseAddr(nd.EgressIP); err == nil {
				entry.SetEgressIP(ip)
				entry.LastEgressUpdate.Store(nd.EgressUpdatedAtNs)
			}
		}
	}
	log.Printf("Loaded %d node dynamic states from cache.db", len(dynamics))
	return nil
}

func restoreBootstrapNodeLatencies(
	engine *state.StateEngine,
	pool *topology.GlobalNodePool,
) error {
	latencies, err := engine.LoadAllNodeLatency()
	if err != nil {
		return fmt.Errorf("load node_latency: %w", err)
	}

	for _, nl := range latencies {
		hash, err := node.ParseHex(nl.NodeHash)
		if err != nil {
			continue
		}
		entry, ok := pool.GetEntry(hash)
		if !ok || entry.LatencyTable == nil {
			continue
		}
		entry.LatencyTable.LoadEntry(nl.Domain, node.DomainLatencyStats{
			Ewma:        time.Duration(nl.EwmaNs),
			LastUpdated: time.Unix(0, nl.LastUpdatedNs),
		})
	}
	log.Printf("Loaded %d latency entries from cache.db", len(latencies))
	return nil
}

// bootstrapNodes loads cached node data from persistence for bootstrap recovery.
// Steps: static nodes → subscription bindings → dynamic state → latency tables.
func bootstrapNodes(
	engine *state.StateEngine,
	pool *topology.GlobalNodePool,
	subManager *topology.SubscriptionManager,
	outboundMgr *outbound.OutboundManager,
	envCfg *config.EnvConfig,
) error {
	hashes, err := loadBootstrapNodeStatics(engine, pool, envCfg)
	if err != nil {
		return err
	}

	warmupBootstrapOutbounds(hashes, outboundMgr)

	if err := restoreBootstrapSubscriptionBindings(engine, pool, subManager); err != nil {
		return err
	}
	if err := restoreBootstrapNodeDynamics(engine, pool); err != nil {
		return err
	}
	if err := restoreBootstrapNodeLatencies(engine, pool); err != nil {
		return err
	}
	return nil
}
