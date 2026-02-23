package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/metrics"
	"github.com/Resinat/Resin/internal/requestlog"
	"github.com/Resinat/Resin/internal/service"
)

// Server wraps the HTTP server and mux for the Resin API.
type Server struct {
	httpServer *http.Server
	mux        *http.ServeMux
}

// NewServer creates a new API server wired with all routes.
// cp may be nil if the control plane is not yet initialized.
func NewServer(
	port int,
	adminToken string,
	systemInfo service.SystemInfo,
	runtimeCfg *atomic.Pointer[config.RuntimeConfig],
	envCfg *config.EnvConfig,
	cp *service.ControlPlaneService,
	apiMaxBodyBytes int64,
	requestlogRepo *requestlog.Repo,
	metricsManager *metrics.Manager,
) *Server {
	return NewServerWithAddress(
		"",
		port,
		adminToken,
		systemInfo,
		runtimeCfg,
		envCfg,
		cp,
		apiMaxBodyBytes,
		requestlogRepo,
		metricsManager,
	)
}

// NewServerWithAddress creates a new API server with an explicit listen address.
func NewServerWithAddress(
	listenAddress string,
	port int,
	adminToken string,
	systemInfo service.SystemInfo,
	runtimeCfg *atomic.Pointer[config.RuntimeConfig],
	envCfg *config.EnvConfig,
	cp *service.ControlPlaneService,
	apiMaxBodyBytes int64,
	requestlogRepo *requestlog.Repo,
	metricsManager *metrics.Manager,
) *Server {
	mux := http.NewServeMux()

	// Public (no auth)
	mux.Handle("GET /healthz", HandleHealthz())

	// Authenticated routes
	authed := http.NewServeMux()
	authed.Handle("GET /api/v1/system/info", HandleSystemInfo(systemInfo))
	authed.Handle("GET /api/v1/system/config", HandleSystemConfig(runtimeCfg))
	authed.Handle("GET /api/v1/system/config/default", HandleSystemDefaultConfig())
	authed.Handle("GET /api/v1/system/config/env", HandleSystemEnvConfig(envCfg))

	if cp != nil {
		// System config mutations.
		authed.Handle("PATCH /api/v1/system/config", HandlePatchSystemConfig(cp))

		// Platforms.
		authed.Handle("GET /api/v1/platforms", HandleListPlatforms(cp))
		authed.Handle("POST /api/v1/platforms", HandleCreatePlatform(cp))
		authed.Handle("POST /api/v1/platforms/preview-filter", HandlePreviewFilter(cp))
		authed.Handle("GET /api/v1/platforms/{id}", HandleGetPlatform(cp))
		authed.Handle("PATCH /api/v1/platforms/{id}", HandleUpdatePlatform(cp))
		authed.Handle("DELETE /api/v1/platforms/{id}", HandleDeletePlatform(cp))
		authed.Handle("POST /api/v1/platforms/{id}/actions/reset-to-default", HandleResetPlatform(cp))
		authed.Handle("POST /api/v1/platforms/{id}/actions/rebuild-routable-view", HandleRebuildPlatform(cp))

		// Leases (under platforms).
		authed.Handle("GET /api/v1/platforms/{id}/leases", HandleListLeases(cp))
		authed.Handle("DELETE /api/v1/platforms/{id}/leases", HandleDeleteAllLeases(cp))
		authed.Handle("GET /api/v1/platforms/{id}/leases/{account}", HandleGetLease(cp))
		authed.Handle("DELETE /api/v1/platforms/{id}/leases/{account}", HandleDeleteLease(cp))
		authed.Handle("GET /api/v1/platforms/{id}/ip-load", HandleIPLoad(cp))

		// Subscriptions.
		authed.Handle("GET /api/v1/subscriptions", HandleListSubscriptions(cp))
		authed.Handle("POST /api/v1/subscriptions", HandleCreateSubscription(cp))
		authed.Handle("GET /api/v1/subscriptions/{id}", HandleGetSubscription(cp))
		authed.Handle("PATCH /api/v1/subscriptions/{id}", HandleUpdateSubscription(cp))
		authed.Handle("DELETE /api/v1/subscriptions/{id}", HandleDeleteSubscription(cp))
		authed.Handle("POST /api/v1/subscriptions/{id}/actions/refresh", HandleRefreshSubscription(cp))
		authed.Handle("POST /api/v1/subscriptions/{id}/actions/cleanup-circuit-open-nodes", HandleCleanupSubscriptionCircuitOpenNodes(cp))

		// Account header rules.
		authed.Handle("GET /api/v1/account-header-rules", HandleListRules(cp))
		// Canonical route (DESIGN.md): url_prefix comes from path parameter only.
		authed.Handle("PUT /api/v1/account-header-rules/{prefix...}", HandleUpsertRule(cp))
		authed.Handle("POST /api/v1/account-header-rules:resolve", HandleResolveRule(cp))
		authed.Handle("DELETE /api/v1/account-header-rules/{prefix...}", HandleDeleteRule(cp))

		// Nodes.
		authed.Handle("GET /api/v1/nodes", HandleListNodes(cp))
		authed.Handle("GET /api/v1/nodes/{hash}", HandleGetNode(cp))
		authed.Handle("POST /api/v1/nodes/{hash}/actions/probe-egress", HandleProbeEgress(cp))
		authed.Handle("POST /api/v1/nodes/{hash}/actions/probe-latency", HandleProbeLatency(cp))

		// GeoIP.
		authed.Handle("GET /api/v1/geoip/status", HandleGeoIPStatus(cp))
		authed.Handle("GET /api/v1/geoip/lookup", HandleGeoIPLookup(cp))
		authed.Handle("POST /api/v1/geoip/lookup", HandleGeoIPLookupPost(cp))
		authed.Handle("POST /api/v1/geoip/actions/update-now", HandleGeoIPUpdate(cp))
	}

	// Request log endpoints (always registered if repo is available).
	if requestlogRepo != nil {
		authed.Handle("GET /api/v1/request-logs", HandleListRequestLogs(requestlogRepo))
		authed.Handle("GET /api/v1/request-logs/{log_id}", HandleGetRequestLog(requestlogRepo))
		authed.Handle("GET /api/v1/request-logs/{log_id}/payloads", HandleGetRequestLogPayloads(requestlogRepo))
	}

	// Metrics endpoints.
	if metricsManager != nil {
		// Realtime (ring buffer).
		authed.Handle("GET /api/v1/metrics/realtime/throughput", HandleRealtimeThroughput(metricsManager))
		authed.Handle("GET /api/v1/metrics/realtime/connections", HandleRealtimeConnections(metricsManager))
		authed.Handle("GET /api/v1/metrics/realtime/leases", HandleRealtimeLeases(metricsManager))

		// History (metrics.db bucket).
		authed.Handle("GET /api/v1/metrics/history/traffic", HandleHistoryTraffic(metricsManager))
		authed.Handle("GET /api/v1/metrics/history/requests", HandleHistoryRequests(metricsManager))
		authed.Handle("GET /api/v1/metrics/history/access-latency", HandleHistoryAccessLatency(metricsManager))
		authed.Handle("GET /api/v1/metrics/history/probes", HandleHistoryProbes(metricsManager))
		authed.Handle("GET /api/v1/metrics/history/node-pool", HandleHistoryNodePool(metricsManager))
		authed.Handle("GET /api/v1/metrics/history/lease-lifetime", HandleHistoryLeaseLifetime(metricsManager))

		// Snapshots (realtime computed).
		authed.Handle("GET /api/v1/metrics/snapshots/node-pool", HandleSnapshotNodePool(metricsManager))
		authed.Handle("GET /api/v1/metrics/snapshots/platform-node-pool", HandleSnapshotPlatformNodePool(metricsManager))
		authed.Handle("GET /api/v1/metrics/snapshots/node-latency-distribution", HandleSnapshotNodeLatencyDistribution(metricsManager))
	}

	limitedAuthed := RequestBodyLimitMiddleware(apiMaxBodyBytes, authed)
	mux.Handle("/api/", AuthMiddleware(adminToken, limitedAuthed))
	registerEmbeddedWebUI(mux)

	srv := &http.Server{
		Addr:    net.JoinHostPort(listenAddress, strconv.Itoa(port)),
		Handler: mux,
	}

	return &Server{
		httpServer: srv,
		mux:        mux,
	}
}

// ListenAndServe starts the HTTP server. It blocks until the server stops.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the underlying http.Handler for testing.
func (s *Server) Handler() http.Handler {
	return s.mux
}
