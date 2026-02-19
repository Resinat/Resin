package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/puzpuzpuz/xsync/v4"

	"github.com/resin-proxy/resin/internal/api"
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
	accountMatcher := buildAccountMatcher(engine)

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
		NodeTagResolver: topoRuntime.pool.ResolveNodeDisplayTag,
		// Lease events are emitted synchronously on routing paths.
		// Keep this callback lightweight and non-blocking.
		// metricsManager is set after creation below via leaseEventMetricsSink.
		OnLeaseEvent: func(e routing.LeaseEvent) {
			switch e.Type {
			case routing.LeaseCreate, routing.LeaseTouch, routing.LeaseReplace:
				engine.MarkLease(e.PlatformID, e.Account)
			case routing.LeaseRemove, routing.LeaseExpire:
				engine.MarkLeaseDelete(e.PlatformID, e.Account)
			}
			if sink := leaseEventMetricsSink.Load(); sink != nil {
				op := metrics.LeaseOpTouch
				switch e.Type {
				case routing.LeaseCreate:
					op = metrics.LeaseOpCreate
				case routing.LeaseReplace:
					op = metrics.LeaseOpReplace
				case routing.LeaseRemove:
					op = metrics.LeaseOpRemove
				case routing.LeaseExpire:
					op = metrics.LeaseOpExpire
				}
				lifetimeNs := int64(0)
				if e.CreatedAtNs > 0 && op.HasLifetimeSample() {
					lifetimeNs = time.Now().UnixNano() - e.CreatedAtNs
				}
				(*sink).OnLeaseEvent(metrics.LeaseMetricEvent{
					PlatformID: e.PlatformID,
					Op:         op,
					LifetimeNs: lifetimeNs,
				})
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

	// Phase 6.1: Bootstrap nodes (steps 3-6: static, subscription_nodes, dynamic, latency).
	if err := bootstrapNodes(engine, topoRuntime.pool, topoRuntime.subManager, topoRuntime.outboundMgr, envCfg); err != nil {
		fatalf("%v", err)
	}

	// GeoIP moved to step 8 batch 1 (after lease restore, per DESIGN.md).

	// Phase 8: Outbound warmup — create outbounds for all bootstrapped nodes.
	topoRuntime.outboundMgr.WarmupAll()

	// Phase 8.1: Rebuild platform views BEFORE lease restore.
	// DESIGN.md requires step 6 (rebuild) before step 7 (leases).
	topoRuntime.pool.RebuildAllPlatforms()
	log.Println("Outbound warmup and platform rebuild complete")

	// Phase 9: Restore leases (AFTER rebuild so platform views are populated).
	leases, err := engine.LoadAllLeases()
	if err != nil {
		log.Printf("Warning: load leases: %v", err)
	} else if len(leases) > 0 {
		topoRuntime.router.RestoreLeases(leases)
		log.Printf("Restored %d leases from cache.db", len(leases))
	}

	flushReaders := newFlushReaders(topoRuntime.pool, topoRuntime.subManager, topoRuntime.router)
	flushWorker := state.NewCacheFlushWorker(
		engine, flushReaders,
		func() int { return runtimeConfigSnapshot(runtimeCfg).CacheFlushDirtyThreshold },
		func() time.Duration { return time.Duration(runtimeConfigSnapshot(runtimeCfg).CacheFlushInterval) },
		5*time.Second, // check tick
	)
	flushWorker.Start()
	log.Println("Cache flush worker started")

	// Phase 10: Initialize observability services.
	requestLogCfg := deriveRequestLogRuntimeSettings(envCfg)
	metricsCfg := deriveMetricsManagerSettings(envCfg)
	metricsDB, err := metrics.NewMetricsRepo(filepath.Join(envCfg.LogDir, "metrics.db"))
	if err != nil {
		fatalf("metrics DB: %v", err)
	}
	metricsManager := metrics.NewManager(metrics.ManagerConfig{
		Repo:                        metricsDB,
		LatencyBinMs:                metricsCfg.LatencyBinMs,
		LatencyOverflowMs:           metricsCfg.LatencyOverflowMs,
		BucketSeconds:               metricsCfg.BucketSeconds,
		ThroughputRealtimeCapacity:  metricsCfg.ThroughputRealtimeCapacity,
		ThroughputIntervalSec:       metricsCfg.ThroughputIntervalSec,
		ConnectionsRealtimeCapacity: metricsCfg.ConnectionsRealtimeCapacity,
		ConnectionsIntervalSec:      metricsCfg.ConnectionsIntervalSec,
		LeasesRealtimeCapacity:      metricsCfg.LeasesRealtimeCapacity,
		LeasesIntervalSec:           metricsCfg.LeasesIntervalSec,
		NodePoolStats:               &nodePoolStatsAdapter{pool: topoRuntime.pool},
		LeaseCountProvider: &leaseCountAdapter{
			pool:   topoRuntime.pool,
			router: topoRuntime.router,
		},
		PlatformStats: &platformStatsAdapter{pool: topoRuntime.pool},
		NodeLatency: &nodeLatencyAdapter{
			pool: topoRuntime.pool,
			authorities: func() []string {
				return runtimeConfigSnapshot(runtimeCfg).LatencyAuthorities
			},
		},
	})

	// Wire LeaseEvent metrics sink (closure captures metricsManager).
	leaseEventMetricsSink.Store(&metricsManager)

	requestlogRepo := requestlog.NewRepo(
		envCfg.LogDir,
		requestLogCfg.DBMaxBytes,
		requestLogCfg.DBRetainCount,
	)
	if err := requestlogRepo.Open(); err != nil {
		fatalf("requestlog repo open: %v", err)
	}
	requestlogSvc := requestlog.NewService(requestlog.ServiceConfig{
		Repo:          requestlogRepo,
		QueueSize:     requestLogCfg.QueueSize,
		FlushBatch:    requestLogCfg.FlushBatch,
		FlushInterval: requestLogCfg.FlushInterval,
	})

	// --- Step 8 Batch 1: CacheFlushWorker (already started) + GeoIP + MetricsManager ---
	startGeoIPService(geoSvc)
	log.Println("GeoIP service started (batch 1)")

	metricsManager.Start()
	log.Println("Metrics manager started (batch 1)")

	// --- Step 8 Batch 2: ProbeManager, RequestLog, LeaseCleaner, EphemeralCleaner ---
	topoRuntime.probeMgr.SetOnProbeEvent(func(kind string) {
		metricsManager.OnProbeEvent(metrics.ProbeEvent{Kind: metrics.ProbeKind(kind)})
	})

	topoRuntime.probeMgr.Start()
	log.Println("Probe manager started (batch 2)")

	requestlogSvc.Start()
	log.Println("Request log service started (batch 2)")

	topoRuntime.leaseCleaner.Start()
	log.Println("Lease cleaner started (batch 2)")

	topoRuntime.ephemeralCleaner.Start()
	log.Println("Ephemeral cleaner started (batch 2)")

	// --- Step 8 Batch 3: Subscription scheduler (force full refresh on start) ---
	topoRuntime.scheduler.Start()
	topoRuntime.scheduler.ForceRefreshAll()
	log.Println("Subscription scheduler started with forced full refresh (batch 3)")

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
		requestlogRepo,
		metricsManager,
	)

	serverErrCh := make(chan error, 1)
	reportServerErr := func(name string, err error) {
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return
		}
		wrapped := fmt.Errorf("%s: %w", name, err)
		select {
		case serverErrCh <- wrapped:
		default:
		}
	}

	go func() {
		log.Printf("Resin API server starting on :%d", envCfg.APIPort)
		reportServerErr("api server", srv.ListenAndServe())
	}()

	// Composite emitter: requestlog handles EmitRequestLog, metricsManager handles EmitRequestFinished.
	composite := compositeEmitter{logSvc: requestlogSvc, metricsMgr: metricsManager}
	proxyEvents := proxy.ConfigAwareEventEmitter{
		Base: composite,
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
		ProxyToken:  envCfg.ProxyToken,
		Router:      topoRuntime.router,
		Pool:        topoRuntime.pool,
		Health:      topoRuntime.pool,
		Events:      proxyEvents,
		MetricsSink: metricsManager,
	})
	forwardLn, err := net.Listen("tcp", fmt.Sprintf(":%d", envCfg.ForwardProxyPort))
	if err != nil {
		fatalf("forward proxy listen: %v", err)
	}
	forwardLn = proxy.NewCountingListener(forwardLn, metricsManager)
	forwardSrv := &http.Server{Handler: forwardProxy}
	go func() {
		log.Printf("Forward proxy starting on :%d", envCfg.ForwardProxyPort)
		reportServerErr("forward proxy", forwardSrv.Serve(forwardLn))
	}()

	reverseProxy := proxy.NewReverseProxy(proxy.ReverseProxyConfig{
		ProxyToken:     envCfg.ProxyToken,
		Router:         topoRuntime.router,
		Pool:           topoRuntime.pool,
		PlatformLookup: topoRuntime.pool,
		Health:         topoRuntime.pool,
		Matcher:        accountMatcher,
		Events:         proxyEvents,
		MetricsSink:    metricsManager,
	})
	reverseLn, err := net.Listen("tcp", fmt.Sprintf(":%d", envCfg.ReverseProxyPort))
	if err != nil {
		fatalf("reverse proxy listen: %v", err)
	}
	reverseLn = proxy.NewCountingListener(reverseLn, metricsManager)
	reverseSrv := &http.Server{Handler: reverseProxy}
	go func() {
		log.Printf("Reverse proxy starting on :%d", envCfg.ReverseProxyPort)
		reportServerErr("reverse proxy", reverseSrv.Serve(reverseLn))
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	var runtimeErr error
	select {
	case sig := <-quit:
		log.Printf("Received signal %s, shutting down...", sig)
	case err := <-serverErrCh:
		runtimeErr = err
		log.Printf("Received server runtime error (%v), shutting down...", err)
	}

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

	// Stop in order: event sources first, then sinks, then persistence.
	// 1. Stop all event sources (no more events after this).
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

	// 2. Stop observability sinks (flush remaining data).
	requestlogSvc.Stop()
	log.Println("Request log service stopped")
	if err := requestlogRepo.Close(); err != nil {
		log.Printf("Request log repo close error: %v", err)
	}
	log.Println("Request log repo closed")

	metricsManager.Stop()
	log.Println("Metrics manager stopped")
	if err := metricsDB.Close(); err != nil {
		log.Printf("Metrics DB close error: %v", err)
	}
	log.Println("Metrics DB closed")

	// 3. Stop infrastructure.
	if topoRuntime.singboxBuilder != nil {
		if err := topoRuntime.singboxBuilder.Close(); err != nil {
			log.Printf("SingboxBuilder close error: %v", err)
		}
		log.Println("SingboxBuilder stopped")
	}

	flushWorker.Stop() // final cache flush before DB close
	log.Println("Server stopped")
	if runtimeErr != nil {
		_ = dbCloser.Close()
		fatalf("runtime server error: %v", runtimeErr)
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
	throughputInterval := envCfg.MetricThroughputIntervalSeconds
	if throughputInterval <= 0 {
		throughputInterval = 1
	}
	connectionsInterval := envCfg.MetricConnectionsIntervalSeconds
	if connectionsInterval <= 0 {
		connectionsInterval = 5
	}
	leasesInterval := envCfg.MetricLeasesIntervalSeconds
	if leasesInterval <= 0 {
		leasesInterval = 5
	}

	return metricsManagerSettings{
		LatencyBinMs:                envCfg.MetricLatencyBinWidthMS,
		LatencyOverflowMs:           envCfg.MetricLatencyBinOverflowMS,
		BucketSeconds:               envCfg.MetricBucketSeconds,
		ThroughputIntervalSec:       throughputInterval,
		ThroughputRealtimeCapacity:  realtimeCapacity(envCfg.MetricThroughputRetentionSeconds, throughputInterval),
		ConnectionsIntervalSec:      connectionsInterval,
		ConnectionsRealtimeCapacity: realtimeCapacity(envCfg.MetricConnectionsRetentionSeconds, connectionsInterval),
		LeasesIntervalSec:           leasesInterval,
		LeasesRealtimeCapacity:      realtimeCapacity(envCfg.MetricLeasesRetentionSeconds, leasesInterval),
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
				OnConnLifecycle: func(op string) {
					if sink := leaseEventMetricsSink.Load(); sink != nil {
						switch op {
						case "open":
							(*sink).OnConnectionLifecycle(proxy.ConnectionOutbound, proxy.ConnectionOpen)
						case "close":
							(*sink).OnConnectionLifecycle(proxy.ConnectionOutbound, proxy.ConnectionClose)
						}
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

// leaseEventMetricsSink is set after metricsManager creation so that the
// lease event callback (set during router creation) can forward events.
var leaseEventMetricsSink atomic.Pointer[*metrics.Manager]

// --- Metrics provider adapters ---

// nodePoolStatsAdapter implements metrics.NodePoolStatsProvider using GlobalNodePool.
type nodePoolStatsAdapter struct {
	pool *topology.GlobalNodePool
}

func (a *nodePoolStatsAdapter) TotalNodes() int { return a.pool.Size() }

func (a *nodePoolStatsAdapter) HealthyNodes() int {
	count := 0
	a.pool.RangeNodes(func(_ node.Hash, entry *node.NodeEntry) bool {
		if !entry.IsCircuitOpen() && entry.HasOutbound() {
			count++
		}
		return true
	})
	return count
}

func (a *nodePoolStatsAdapter) EgressIPCount() int {
	seen := make(map[netip.Addr]struct{})
	a.pool.RangeNodes(func(_ node.Hash, entry *node.NodeEntry) bool {
		if ip := entry.GetEgressIP(); ip.IsValid() {
			seen[ip] = struct{}{}
		}
		return true
	})
	return len(seen)
}

// leaseCountAdapter implements metrics.LeaseCountProvider using Router.
type leaseCountAdapter struct {
	pool   *topology.GlobalNodePool
	router *routing.Router
}

func (a *leaseCountAdapter) LeaseCountsByPlatform() map[string]int {
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

// platformStatsAdapter implements metrics.PlatformStatsProvider using GlobalNodePool.
type platformStatsAdapter struct {
	pool *topology.GlobalNodePool
}

func (a *platformStatsAdapter) RoutableNodeCount(platformID string) (int, bool) {
	plat, ok := a.pool.GetPlatform(platformID)
	if !ok {
		return 0, false
	}
	return plat.View().Size(), true
}

func (a *platformStatsAdapter) PlatformEgressIPCount(platformID string) (int, bool) {
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

// nodeLatencyAdapter implements metrics.NodeLatencyProvider using GlobalNodePool.
type nodeLatencyAdapter struct {
	pool        *topology.GlobalNodePool
	authorities func() []string
}

func (a *nodeLatencyAdapter) CollectNodeEWMAs(platformID string) []float64 {
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
	c.metricsMgr.EmitRequestFinished(ev)
}

func (c compositeEmitter) EmitRequestLog(ev proxy.RequestLogEntry) {
	c.logSvc.EmitRequestLog(ev)
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
	// Step 3: Load static nodes.
	statics, err := engine.LoadAllNodesStatic()
	if err != nil {
		return fmt.Errorf("load nodes_static: %w", err)
	}
	for _, ns := range statics {
		hash, err := node.ParseHex(ns.Hash)
		if err != nil {
			log.Printf("[bootstrap] skip node %s: %v", ns.Hash, err)
			continue
		}
		entry := &node.NodeEntry{
			Hash:       hash,
			RawOptions: json.RawMessage(ns.RawOptionsJSON),
			CreatedAt:  time.Unix(0, ns.CreatedAtNs),
		}
		entry.LatencyTable = node.NewLatencyTable(envCfg.MaxLatencyTableEntries)
		pool.LoadNodeFromBootstrap(entry)
	}
	log.Printf("Loaded %d static nodes from cache.db", len(statics))

	// Parallel outbound init for bootstrapped nodes.
	if len(statics) > 0 {
		workers := runtime.GOMAXPROCS(0)
		if workers < 1 {
			workers = 1
		}
		hashCh := make(chan node.Hash, len(statics))
		for _, ns := range statics {
			hash, err := node.ParseHex(ns.Hash)
			if err != nil {
				continue
			}
			hashCh <- hash
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

	// Step 4: Load subscription-node bindings.
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
			var tags []string
			if sn.TagsJSON != "" {
				_ = json.Unmarshal([]byte(sn.TagsJSON), &tags)
			}
			managed.Store(hash, tags)
			// Also set the node's subscription ID reference.
			if entry, ok := pool.GetEntry(hash); ok {
				entry.AddSubscriptionID(subID)
			}
		}
		sub.SwapManagedNodes(managed)
	}
	log.Printf("Loaded %d subscription-node bindings from cache.db", len(subNodes))

	// Step 5: Load dynamic state (failure count, circuit breaker, egress IP).
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

	// Step 6: Load latency tables.
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
		if !ok {
			continue
		}
		if entry.LatencyTable != nil {
			entry.LatencyTable.LoadEntry(nl.Domain, node.DomainLatencyStats{
				Ewma:        time.Duration(nl.EwmaNs),
				LastUpdated: time.Unix(0, nl.LastUpdatedNs),
			})
		}
	}
	log.Printf("Loaded %d latency entries from cache.db", len(latencies))
	return nil
}
