package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/resin-proxy/resin/internal/api"
	"github.com/resin-proxy/resin/internal/buildinfo"
	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/geoip"
	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/netutil"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/outbound"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/probe"
	"github.com/resin-proxy/resin/internal/proxy"
	"github.com/resin-proxy/resin/internal/routing"
	"github.com/resin-proxy/resin/internal/service"
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
	envCfg, err := config.LoadEnvConfig()
	if err != nil {
		fatalf("%v", err)
	}

	engine, dbCloser, err := state.PersistenceBootstrap(envCfg.StateDir, envCfg.CacheDir)
	if err != nil {
		fatalf("persistence bootstrap: %v", err)
	}
	defer dbCloser.Close()
	log.Println("Persistence bootstrap complete")

	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	runtimeCfg.Store(loadRuntimeConfig(engine))

	// Phase 1: Create DirectDownloader and RetryDownloader shell.
	// NodePicker/ProxyFetch are nil initially; set after Pool + OutboundManager creation.
	direct := newDirectDownloader(envCfg, runtimeCfg)
	retryDL := &netutil.RetryDownloader{
		Direct: direct,
	}

	// Phase 2: Construct GeoIP service (start after retry downloader wiring).
	geoSvc := newGeoIPService(envCfg.CacheDir, envCfg.GeoIPUpdateSchedule, retryDL)

	// Phase 3: Topology (pool, probe, scheduler).
	topoRuntime, err := newTopologyRuntime(engine, envCfg, runtimeCfg, geoSvc, retryDL)
	if err != nil {
		fatalf("topology runtime: %v", err)
	}

	// Phase 4: OutboundManager and Router (now that pool exists).
	log.Println("OutboundManager initialized with lifecycle callbacks")

	topoRuntime.router = routing.NewRouter(routing.RouterConfig{
		Pool: topoRuntime.pool,
		Authorities: func() []string {
			return runtimeConfigSnapshot(runtimeCfg).LatencyAuthorities
		},
		P2CWindow: func() time.Duration {
			return time.Duration(runtimeConfigSnapshot(runtimeCfg).P2CLatencyWindow)
		},
		// Lease events are emitted synchronously on routing paths.
		// Keep this callback lightweight and non-blocking.
		OnLeaseEvent: func(e routing.LeaseEvent) {
			switch e.Type {
			case routing.LeaseCreate, routing.LeaseTouch, routing.LeaseReplace:
				engine.MarkLease(e.PlatformID, e.Account)
			case routing.LeaseRemove, routing.LeaseExpire:
				engine.MarkLeaseDelete(e.PlatformID, e.Account)
			}
		},
	})
	topoRuntime.leaseCleaner = routing.NewLeaseCleaner(topoRuntime.router)
	log.Println("Router and LeaseCleaner initialized")

	// Phase 5: Complete RetryDownloader wiring (now that Pool + OutboundManager exist).
	retryDL.NodePicker = func(target string) (node.Hash, error) {
		res, err := topoRuntime.router.RouteRequest("", "", target)
		if err != nil {
			return node.Zero, err
		}
		return res.NodeHash, nil
	}
	retryDL.ProxyFetch = func(ctx context.Context, hash node.Hash, url string) ([]byte, error) {
		body, _, err := topoRuntime.outboundMgr.FetchWithUserAgent(ctx, hash, url, currentDownloadUserAgent(runtimeCfg))
		return body, err
	}
	log.Println("RetryDownloader wiring complete")

	// Phase 6: Bootstrap topology data from persistence.
	if err := bootstrapTopology(engine, topoRuntime.subManager, topoRuntime.pool, envCfg); err != nil {
		fatalf("%v", err)
	}

	// Phase 6.5: Start GeoIP after platforms are loaded so proxy retry can
	// route through the restored Default platform immediately.
	startGeoIPService(geoSvc)

	// Phase 7: Restore leases.
	leases, err := engine.LoadAllLeases()
	if err != nil {
		log.Printf("Warning: load leases: %v", err)
	} else if len(leases) > 0 {
		topoRuntime.router.RestoreLeases(leases)
		log.Printf("Restored %d leases from cache.db", len(leases))
	}

	// Phase 8: Outbound warmup — create outbounds for all bootstrapped nodes.
	topoRuntime.outboundMgr.WarmupAll()
	topoRuntime.pool.RebuildAllPlatforms()
	log.Println("Outbound warmup complete")

	flushReaders := newFlushReaders(topoRuntime.pool, topoRuntime.subManager, topoRuntime.router)
	flushWorker := state.NewCacheFlushWorker(
		engine, flushReaders,
		func() int { return runtimeConfigSnapshot(runtimeCfg).CacheFlushDirtyThreshold },
		func() time.Duration { return time.Duration(runtimeConfigSnapshot(runtimeCfg).CacheFlushInterval) },
		5*time.Second, // check tick
	)
	flushWorker.Start()
	log.Println("Cache flush worker started")

	topoRuntime.probeMgr.Start()
	log.Println("Probe manager started")

	topoRuntime.scheduler.Start()
	log.Println("Subscription scheduler started")

	topoRuntime.ephemeralCleaner.Start()
	log.Println("Ephemeral cleaner started")

	topoRuntime.leaseCleaner.Start()
	log.Println("Lease cleaner started")

	startedAt := time.Now().UTC()
	systemSvc := service.NewMemorySystemService(
		service.SystemInfo{
			Version:   buildinfo.Version,
			GitCommit: buildinfo.GitCommit,
			BuildTime: buildinfo.BuildTime,
			StartedAt: startedAt,
		},
		runtimeCfg,
	)

	// Phase 6: Load Account Header Rules and start Forward/Reverse Proxy.
	accountMatcher := buildAccountMatcher(engine)

	// Build the Control Plane service (after accountMatcher so we can inject MatcherRuntime).
	cpService := &service.ControlPlaneService{
		RuntimeCfg:     runtimeCfg,
		EnvCfg:         envCfg,
		Engine:         engine,
		Pool:           topoRuntime.pool,
		SubMgr:         topoRuntime.subManager,
		Scheduler:      topoRuntime.scheduler,
		Router:         topoRuntime.router,
		ProbeMgr:       topoRuntime.probeMgr,
		GeoIP:          geoSvc,
		MatcherRuntime: accountMatcher,
	}

	srv := api.NewServer(
		envCfg.APIPort,
		envCfg.AdminToken,
		systemSvc,
		cpService,
		int64(envCfg.APIMaxBodyBytes),
	)

	go func() {
		log.Printf("Resin API server starting on :%d", envCfg.APIPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("API server error: %v", err)
		}
	}()

	proxyEvents := proxy.ConfigAwareEventEmitter{
		Base: proxy.NoOpEventEmitter{},
		RequestLogEnabled: func() bool {
			return runtimeConfigSnapshot(runtimeCfg).RequestLogEnabled
		},
		ReverseProxyLogDetailEnabled: func() bool {
			return runtimeConfigSnapshot(runtimeCfg).ReverseProxyLogDetailEnabled
		},
		ReverseProxyLogReqHeadersMaxBytes: func() int {
			return runtimeConfigSnapshot(runtimeCfg).ReverseProxyLogReqHeadersMaxBytes
		},
		ReverseProxyLogReqBodyMaxBytes: func() int {
			return runtimeConfigSnapshot(runtimeCfg).ReverseProxyLogReqBodyMaxBytes
		},
		ReverseProxyLogRespHeadersMaxBytes: func() int {
			return runtimeConfigSnapshot(runtimeCfg).ReverseProxyLogRespHeadersMaxBytes
		},
		ReverseProxyLogRespBodyMaxBytes: func() int {
			return runtimeConfigSnapshot(runtimeCfg).ReverseProxyLogRespBodyMaxBytes
		},
	}

	forwardProxy := proxy.NewForwardProxy(proxy.ForwardProxyConfig{
		ProxyToken: envCfg.ProxyToken,
		Router:     topoRuntime.router,
		Pool:       topoRuntime.pool,
		Health:     topoRuntime.pool,
		Events:     proxyEvents,
	})
	forwardSrv := &http.Server{
		Addr:    fmt.Sprintf(":%d", envCfg.ForwardProxyPort),
		Handler: forwardProxy,
	}
	go func() {
		log.Printf("Forward proxy starting on :%d", envCfg.ForwardProxyPort)
		if err := forwardSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Forward proxy error: %v", err)
		}
	}()

	reverseProxy := proxy.NewReverseProxy(proxy.ReverseProxyConfig{
		ProxyToken:     envCfg.ProxyToken,
		Router:         topoRuntime.router,
		Pool:           topoRuntime.pool,
		PlatformLookup: topoRuntime.pool,
		Health:         topoRuntime.pool,
		Matcher:        accountMatcher,
		Events:         proxyEvents,
	})
	reverseSrv := &http.Server{
		Addr:    fmt.Sprintf(":%d", envCfg.ReverseProxyPort),
		Handler: reverseProxy,
	}
	go func() {
		log.Printf("Reverse proxy starting on :%d", envCfg.ReverseProxyPort)
		if err := reverseSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Reverse proxy error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("Received signal %s, shutting down...", sig)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := forwardSrv.Shutdown(ctx); err != nil {
		log.Printf("Forward proxy shutdown error: %v", err)
	}
	log.Println("Forward proxy stopped")

	if err := reverseSrv.Shutdown(ctx); err != nil {
		log.Printf("Reverse proxy shutdown error: %v", err)
	}
	log.Println("Reverse proxy stopped")

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	// Stop in reverse order: lease cleaner -> ephemeral cleaner -> scheduler -> probe manager -> geoip -> flush worker.
	// scheduler must stop before probe manager to avoid post-stop triggers.
	topoRuntime.leaseCleaner.Stop()
	log.Println("Lease cleaner stopped")

	topoRuntime.ephemeralCleaner.Stop()
	log.Println("Ephemeral cleaner stopped")

	topoRuntime.scheduler.Stop()
	log.Println("Subscription scheduler stopped")

	topoRuntime.probeMgr.Stop()
	log.Println("Probe manager stopped")

	geoSvc.Stop()
	log.Println("GeoIP service stopped")

	if topoRuntime.singboxBuilder != nil {
		if err := topoRuntime.singboxBuilder.Close(); err != nil {
			log.Printf("SingboxBuilder close error: %v", err)
		}
		log.Println("SingboxBuilder stopped")
	}

	flushWorker.Stop() // final flush before DB close
	log.Println("Server stopped")
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
		engine.MarkNodeStaticDelete(hash.Hex())
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

	regexJSON, err := json.Marshal(envCfg.DefaultPlatformRegexFilters)
	if err != nil {
		return fmt.Errorf("marshal default regex_filters: %w", err)
	}
	regionJSON, err := json.Marshal(envCfg.DefaultPlatformRegionFilters)
	if err != nil {
		return fmt.Errorf("marshal default region_filters: %w", err)
	}

	defaultPlatform := model.Platform{
		ID:                     platform.DefaultPlatformID,
		Name:                   platform.DefaultPlatformName,
		StickyTTLNs:            int64(envCfg.DefaultPlatformStickyTTL),
		RegexFiltersJSON:       string(regexJSON),
		RegionFiltersJSON:      string(regionJSON),
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
				Hash:           hash,
				RawOptionsJSON: string(entry.RawOptions),
				CreatedAtNs:    entry.CreatedAt.UnixNano(),
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
			tagsJSONBytes, _ := json.Marshal(tags)
			return &model.SubscriptionNode{
				SubscriptionID: key.SubscriptionID,
				NodeHash:       key.NodeHash,
				TagsJSON:       string(tagsJSONBytes),
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
