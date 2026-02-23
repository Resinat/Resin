package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Resinat/Resin/internal/api"
	"github.com/Resinat/Resin/internal/buildinfo"
	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/geoip"
	"github.com/Resinat/Resin/internal/metrics"
	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/proxy"
	"github.com/Resinat/Resin/internal/requestlog"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/service"
	"github.com/Resinat/Resin/internal/state"
)

type resinApp struct {
	envCfg         *config.EnvConfig
	runtimeCfg     *atomic.Pointer[config.RuntimeConfig]
	accountMatcher *proxy.AccountMatcherRuntime
	geoSvc         *geoip.Service
	topoRuntime    *topologyRuntime
	flushWorker    *state.CacheFlushWorker
	metricsDB      *metrics.MetricsRepo
	metricsManager *metrics.Manager
	requestlogRepo *requestlog.Repo
	requestlogSvc  *requestlog.Service
	inboundSrv     *http.Server
	inboundLn      net.Listener
	transportPool  *proxy.OutboundTransportPool
}

func run() error {
	envCfg, err := config.LoadEnvConfig()
	if err != nil {
		return err
	}

	engine, dbCloser, err := state.PersistenceBootstrap(envCfg.StateDir, envCfg.CacheDir)
	if err != nil {
		return fmt.Errorf("persistence bootstrap: %w", err)
	}
	log.Println("Persistence bootstrap complete")

	app, err := newResinApp(envCfg, engine)
	if err != nil {
		_ = dbCloser.Close()
		return err
	}

	serverErrCh := app.startServers()
	runtimeErr := waitForShutdown(serverErrCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	app.shutdown(ctx)

	if err := dbCloser.Close(); err != nil {
		log.Printf("Persistence close error: %v", err)
	}
	if runtimeErr != nil {
		return fmt.Errorf("runtime server error: %w", runtimeErr)
	}
	return nil
}

func newResinApp(envCfg *config.EnvConfig, engine *state.StateEngine) (*resinApp, error) {
	app := &resinApp{
		envCfg:     envCfg,
		runtimeCfg: &atomic.Pointer[config.RuntimeConfig]{},
	}
	app.runtimeCfg.Store(loadRuntimeConfig(engine))
	if err := ensureDefaultAccountHeaderRule(engine); err != nil {
		return nil, err
	}
	app.accountMatcher = buildAccountMatcher(engine)

	retryDL, err := app.initTopologyRuntime(engine)
	if err != nil {
		return nil, err
	}
	app.wireRetryDownloader(retryDL)

	if err := app.bootstrapFromPersistence(engine); err != nil {
		return nil, err
	}
	if err := app.initObservability(); err != nil {
		return nil, err
	}
	if err := app.buildNetworkServers(engine); err != nil {
		return nil, err
	}

	app.startBackgroundServices()
	return app, nil
}

func (a *resinApp) initTopologyRuntime(engine *state.StateEngine) (*netutil.RetryDownloader, error) {
	// Phase 1: Create DirectDownloader and RetryDownloader shell.
	// NodePicker/ProxyFetch are nil initially; set after Pool + OutboundManager creation.
	direct := newDirectDownloader(a.envCfg, a.runtimeCfg)
	retryDL := &netutil.RetryDownloader{Direct: direct}

	// Phase 2: Construct GeoIP service (start after retry downloader wiring).
	a.geoSvc = newGeoIPService(a.envCfg.CacheDir, a.envCfg.GeoIPUpdateSchedule, retryDL)

	// Phase 3: Topology (pool, probe, scheduler).
	topoRuntime, err := newTopologyRuntime(
		engine,
		a.envCfg,
		a.runtimeCfg,
		a.geoSvc,
		retryDL,
		a.onProbeConnectionLifecycle,
		func(hash node.Hash) {
			if a.transportPool != nil {
				a.transportPool.Evict(hash)
			}
		},
	)
	if err != nil {
		return nil, fmt.Errorf("topology runtime: %w", err)
	}
	a.topoRuntime = topoRuntime

	// Phase 4: OutboundManager and Router (now that pool exists).
	log.Println("OutboundManager initialized with lifecycle callbacks")
	a.topoRuntime.router = routing.NewRouter(routing.RouterConfig{
		Pool: a.topoRuntime.pool,
		Authorities: func() []string {
			return runtimeConfigSnapshot(a.runtimeCfg).LatencyAuthorities
		},
		P2CWindow: func() time.Duration {
			return time.Duration(runtimeConfigSnapshot(a.runtimeCfg).P2CLatencyWindow)
		},
		NodeTagResolver: a.topoRuntime.pool.ResolveNodeDisplayTag,
		// Lease events are emitted synchronously on routing paths.
		// Keep this callback lightweight and non-blocking.
		OnLeaseEvent: func(e routing.LeaseEvent) {
			switch e.Type {
			case routing.LeaseCreate, routing.LeaseTouch, routing.LeaseReplace:
				engine.MarkLease(e.PlatformID, e.Account)
			case routing.LeaseRemove, routing.LeaseExpire:
				engine.MarkLeaseDelete(e.PlatformID, e.Account)
			}
			a.onLeaseEventForMetrics(e)
		},
	})
	a.topoRuntime.leaseCleaner = routing.NewLeaseCleaner(a.topoRuntime.router)
	log.Println("Router and LeaseCleaner initialized")
	return retryDL, nil
}

func (a *resinApp) onProbeConnectionLifecycle(op netutil.ConnLifecycleOp) {
	if a == nil || a.metricsManager == nil {
		return
	}
	switch op {
	case netutil.ConnLifecycleOpen:
		a.metricsManager.OnConnectionLifecycle(proxy.ConnectionOutbound, proxy.ConnectionOpen)
	case netutil.ConnLifecycleClose:
		a.metricsManager.OnConnectionLifecycle(proxy.ConnectionOutbound, proxy.ConnectionClose)
	}
}

func (a *resinApp) onLeaseEventForMetrics(e routing.LeaseEvent) {
	if a == nil || a.metricsManager == nil {
		return
	}

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

	a.metricsManager.OnLeaseEvent(metrics.LeaseMetricEvent{
		PlatformID: e.PlatformID,
		Op:         op,
		LifetimeNs: lifetimeNs,
	})
}

func (a *resinApp) wireRetryDownloader(retryDL *netutil.RetryDownloader) {
	// Phase 5: Complete RetryDownloader wiring (now that Pool + OutboundManager exist).
	retryDL.NodePicker = func(target string) (node.Hash, error) {
		res, err := a.topoRuntime.router.RouteRequest("", "", target)
		if err != nil {
			return node.Zero, err
		}
		return res.NodeHash, nil
	}
	retryDL.ProxyFetch = func(ctx context.Context, hash node.Hash, url string) ([]byte, error) {
		body, _, err := a.topoRuntime.outboundMgr.FetchWithUserAgent(ctx, hash, url, currentDownloadUserAgent(a.runtimeCfg))
		return body, err
	}
	log.Println("RetryDownloader wiring complete")
}

func (a *resinApp) bootstrapFromPersistence(engine *state.StateEngine) error {
	// Phase 6: Bootstrap topology data from persistence.
	if err := bootstrapTopology(engine, a.topoRuntime.subManager, a.topoRuntime.pool, a.envCfg); err != nil {
		return err
	}

	// Phase 6.1: Bootstrap nodes (steps 3-6: static, subscription_nodes, dynamic, latency).
	if err := bootstrapNodes(engine, a.topoRuntime.pool, a.topoRuntime.subManager, a.topoRuntime.outboundMgr, a.envCfg); err != nil {
		return err
	}

	// GeoIP moved to step 8 batch 1 (after lease restore, per DESIGN.md).

	// Phase 8.1: Rebuild platform views BEFORE lease restore.
	// DESIGN.md requires step 6 (rebuild) before step 7 (leases).
	a.topoRuntime.pool.RebuildAllPlatforms()
	log.Println("Platform rebuild complete")

	// Phase 9: Restore leases (AFTER rebuild so platform views are populated).
	leases, err := engine.LoadAllLeases()
	if err != nil {
		log.Printf("Warning: load leases: %v", err)
	} else if len(leases) > 0 {
		a.topoRuntime.router.RestoreLeases(leases)
		log.Printf("Restored %d leases from cache.db", len(leases))
	}

	flushReaders := newFlushReaders(a.topoRuntime.pool, a.topoRuntime.subManager, a.topoRuntime.router)
	a.flushWorker = state.NewCacheFlushWorker(
		engine,
		flushReaders,
		func() int { return runtimeConfigSnapshot(a.runtimeCfg).CacheFlushDirtyThreshold },
		func() time.Duration { return time.Duration(runtimeConfigSnapshot(a.runtimeCfg).CacheFlushInterval) },
		5*time.Second, // check tick
	)
	return nil
}

func (a *resinApp) initObservability() error {
	// Phase 10: Initialize observability services.
	requestLogCfg := deriveRequestLogRuntimeSettings(a.envCfg)
	metricsCfg := deriveMetricsManagerSettings(a.envCfg)

	metricsDB, err := metrics.NewMetricsRepo(filepath.Join(a.envCfg.LogDir, "metrics.db"))
	if err != nil {
		return fmt.Errorf("metrics DB: %w", err)
	}
	a.metricsDB = metricsDB

	a.metricsManager = metrics.NewManager(metrics.ManagerConfig{
		Repo:                        a.metricsDB,
		LatencyBinMs:                metricsCfg.LatencyBinMs,
		LatencyOverflowMs:           metricsCfg.LatencyOverflowMs,
		BucketSeconds:               metricsCfg.BucketSeconds,
		ThroughputRealtimeCapacity:  metricsCfg.ThroughputRealtimeCapacity,
		ThroughputIntervalSec:       metricsCfg.ThroughputIntervalSec,
		ConnectionsRealtimeCapacity: metricsCfg.ConnectionsRealtimeCapacity,
		ConnectionsIntervalSec:      metricsCfg.ConnectionsIntervalSec,
		LeasesRealtimeCapacity:      metricsCfg.LeasesRealtimeCapacity,
		LeasesIntervalSec:           metricsCfg.LeasesIntervalSec,
		RuntimeStats: &runtimeStatsAdapter{
			pool:   a.topoRuntime.pool,
			router: a.topoRuntime.router,
			authorities: func() []string {
				return runtimeConfigSnapshot(a.runtimeCfg).LatencyAuthorities
			},
		},
	})

	a.requestlogRepo = requestlog.NewRepo(
		a.envCfg.LogDir,
		requestLogCfg.DBMaxBytes,
		requestLogCfg.DBRetainCount,
	)
	if err := a.requestlogRepo.Open(); err != nil {
		return fmt.Errorf("requestlog repo open: %w", err)
	}
	a.requestlogSvc = requestlog.NewService(requestlog.ServiceConfig{
		Repo:          a.requestlogRepo,
		QueueSize:     requestLogCfg.QueueSize,
		FlushBatch:    requestLogCfg.FlushBatch,
		FlushInterval: requestLogCfg.FlushInterval,
	})
	return nil
}

func (a *resinApp) startBackgroundServices() {
	// --- Step 8 Batch 1: CacheFlushWorker + GeoIP + MetricsManager ---
	a.flushWorker.Start()
	log.Println("Cache flush worker started")

	startGeoIPService(a.geoSvc)
	log.Println("GeoIP service started (batch 1)")

	a.metricsManager.Start()
	log.Println("Metrics manager started (batch 1)")

	// --- Step 8 Batch 2: ProbeManager, RequestLog, LeaseCleaner, EphemeralCleaner ---
	a.topoRuntime.probeMgr.SetOnProbeEvent(func(kind string) {
		a.metricsManager.OnProbeEvent(metrics.ProbeEvent{Kind: metrics.ProbeKind(kind)})
	})

	a.topoRuntime.probeMgr.Start()
	log.Println("Probe manager started (batch 2)")

	a.requestlogSvc.Start()
	log.Println("Request log service started (batch 2)")

	a.topoRuntime.leaseCleaner.Start()
	log.Println("Lease cleaner started (batch 2)")

	a.topoRuntime.ephemeralCleaner.Start()
	log.Println("Ephemeral cleaner started (batch 2)")

	// --- Step 8 Batch 3: Subscription scheduler (force full refresh on start) ---
	a.topoRuntime.scheduler.Start()
	a.topoRuntime.scheduler.ForceRefreshAllAsync()
	log.Println("Subscription scheduler started; forced full refresh running in background (batch 3)")
}

func (a *resinApp) buildNetworkServers(engine *state.StateEngine) error {
	startedAt := time.Now().UTC()
	systemInfo := service.SystemInfo{
		Version:   buildinfo.Version,
		GitCommit: buildinfo.GitCommit,
		BuildTime: buildinfo.BuildTime,
		StartedAt: startedAt,
	}

	cpService := &service.ControlPlaneService{
		RuntimeCfg:     a.runtimeCfg,
		EnvCfg:         a.envCfg,
		Engine:         engine,
		Pool:           a.topoRuntime.pool,
		SubMgr:         a.topoRuntime.subManager,
		Scheduler:      a.topoRuntime.scheduler,
		Router:         a.topoRuntime.router,
		ProbeMgr:       a.topoRuntime.probeMgr,
		GeoIP:          a.geoSvc,
		MatcherRuntime: a.accountMatcher,
	}

	apiSrv := api.NewServerWithAddress(
		a.envCfg.ListenAddress,
		a.envCfg.ResinPort,
		a.envCfg.AdminToken,
		systemInfo,
		a.runtimeCfg,
		a.envCfg,
		cpService,
		int64(a.envCfg.APIMaxBodyBytes),
		a.requestlogRepo,
		a.metricsManager,
	)
	tokenActionHandler := api.NewTokenActionHandler(
		a.envCfg.ProxyToken,
		cpService,
		int64(a.envCfg.APIMaxBodyBytes),
	)

	proxyEvents := a.buildProxyEvents()
	outboundTransportCfg := proxy.OutboundTransportConfig{
		MaxIdleConns:        a.envCfg.ProxyTransportMaxIdleConns,
		MaxIdleConnsPerHost: a.envCfg.ProxyTransportMaxIdleConnsPerHost,
		IdleConnTimeout:     a.envCfg.ProxyTransportIdleConnTimeout,
	}
	if a.transportPool == nil {
		a.transportPool = proxy.NewOutboundTransportPool(outboundTransportCfg)
	}

	forwardProxy := proxy.NewForwardProxy(proxy.ForwardProxyConfig{
		ProxyToken:        a.envCfg.ProxyToken,
		Router:            a.topoRuntime.router,
		Pool:              a.topoRuntime.pool,
		Health:            a.topoRuntime.pool,
		Events:            proxyEvents,
		MetricsSink:       a.metricsManager,
		OutboundTransport: outboundTransportCfg,
		TransportPool:     a.transportPool,
	})

	reverseProxy := proxy.NewReverseProxy(proxy.ReverseProxyConfig{
		ProxyToken:        a.envCfg.ProxyToken,
		Router:            a.topoRuntime.router,
		Pool:              a.topoRuntime.pool,
		PlatformLookup:    a.topoRuntime.pool,
		Health:            a.topoRuntime.pool,
		Matcher:           a.accountMatcher,
		Events:            proxyEvents,
		MetricsSink:       a.metricsManager,
		OutboundTransport: outboundTransportCfg,
		TransportPool:     a.transportPool,
	})

	inboundHandler := newInboundMux(
		a.envCfg.ProxyToken,
		forwardProxy,
		reverseProxy,
		apiSrv.Handler(),
		tokenActionHandler,
	)
	inboundLn, err := net.Listen("tcp", formatListenAddress(a.envCfg.ListenAddress, a.envCfg.ResinPort))
	if err != nil {
		return fmt.Errorf("resin server listen: %w", err)
	}
	a.inboundLn = proxy.NewCountingListener(inboundLn, a.metricsManager)
	a.inboundSrv = &http.Server{Handler: inboundHandler}

	return nil
}

func (a *resinApp) buildProxyEvents() proxy.ConfigAwareEventEmitter {
	// Composite emitter: requestlog handles EmitRequestLog, metricsManager handles EmitRequestFinished.
	composite := compositeEmitter{logSvc: a.requestlogSvc, metricsMgr: a.metricsManager}
	return proxy.ConfigAwareEventEmitter{
		Base: composite,
		RequestLogEnabled: func() bool {
			return runtimeConfigSnapshot(a.runtimeCfg).RequestLogEnabled
		},
		ReverseProxyLogDetailEnabled: func() bool {
			return runtimeConfigSnapshot(a.runtimeCfg).ReverseProxyLogDetailEnabled
		},
		ReverseProxyLogReqHeadersMaxBytes: func() int {
			return runtimeConfigSnapshot(a.runtimeCfg).ReverseProxyLogReqHeadersMaxBytes
		},
		ReverseProxyLogReqBodyMaxBytes: func() int {
			return runtimeConfigSnapshot(a.runtimeCfg).ReverseProxyLogReqBodyMaxBytes
		},
		ReverseProxyLogRespHeadersMaxBytes: func() int {
			return runtimeConfigSnapshot(a.runtimeCfg).ReverseProxyLogRespHeadersMaxBytes
		},
		ReverseProxyLogRespBodyMaxBytes: func() int {
			return runtimeConfigSnapshot(a.runtimeCfg).ReverseProxyLogRespBodyMaxBytes
		},
	}
}

func (a *resinApp) startServers() <-chan error {
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
		log.Printf("Resin server starting on %s", formatListenURL(a.envCfg.ListenAddress, a.envCfg.ResinPort))
		reportServerErr("resin server", a.inboundSrv.Serve(a.inboundLn))
	}()

	return serverErrCh
}

func waitForShutdown(serverErrCh <-chan error) error {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case sig := <-quit:
		log.Printf("Received signal %s, shutting down...", sig)
		return nil
	case err := <-serverErrCh:
		log.Printf("Received server runtime error (%v), shutting down...", err)
		return err
	}
}

func formatListenAddress(listenAddress string, port int) string {
	return net.JoinHostPort(listenAddress, strconv.Itoa(port))
}

func formatListenURL(listenAddress string, port int) string {
	return "http://" + formatListenAddress(listenAddress, port)
}

func (a *resinApp) shutdown(ctx context.Context) {
	if err := a.inboundSrv.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Println("Resin server stopped")
	if a.transportPool != nil {
		a.transportPool.CloseAll()
		log.Println("Outbound transport pool closed")
	}

	// Stop in order: event sources first, then sinks, then persistence.
	// 1. Stop all event sources (no more events after this).
	a.topoRuntime.leaseCleaner.Stop()
	log.Println("Lease cleaner stopped")

	a.topoRuntime.ephemeralCleaner.Stop()
	log.Println("Ephemeral cleaner stopped")

	a.topoRuntime.scheduler.Stop()
	log.Println("Subscription scheduler stopped")

	a.topoRuntime.probeMgr.Stop()
	log.Println("Probe manager stopped")

	a.geoSvc.Stop()
	log.Println("GeoIP service stopped")

	// 2. Stop observability sinks (flush remaining data).
	a.requestlogSvc.Stop()
	log.Println("Request log service stopped")
	if err := a.requestlogRepo.Close(); err != nil {
		log.Printf("Request log repo close error: %v", err)
	}
	log.Println("Request log repo closed")

	a.metricsManager.Stop()
	log.Println("Metrics manager stopped")
	if err := a.metricsDB.Close(); err != nil {
		log.Printf("Metrics DB close error: %v", err)
	}
	log.Println("Metrics DB closed")

	// 3. Stop infrastructure.
	if a.topoRuntime.singboxBuilder != nil {
		if err := a.topoRuntime.singboxBuilder.Close(); err != nil {
			log.Printf("SingboxBuilder close error: %v", err)
		}
		log.Println("SingboxBuilder stopped")
	}

	a.flushWorker.Stop() // final cache flush before DB close
	log.Println("Server stopped")
}
