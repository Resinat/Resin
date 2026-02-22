package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/geoip"
	"github.com/resin-proxy/resin/internal/metrics"
	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/proxy"
	"github.com/resin-proxy/resin/internal/requestlog"
	"github.com/resin-proxy/resin/internal/routing"
	"github.com/resin-proxy/resin/internal/service"
	"github.com/resin-proxy/resin/internal/state"
	"github.com/resin-proxy/resin/internal/topology"
)

const testAdminToken = "test-admin-token"

func newControlPlaneTestServer(t *testing.T) (*Server, *service.ControlPlaneService, *atomic.Pointer[config.RuntimeConfig]) {
	return newControlPlaneTestServerWithBodyLimit(t, 1<<20)
}

func newControlPlaneTestServerWithBodyLimit(
	t *testing.T,
	apiMaxBodyBytes int64,
) (*Server, *service.ControlPlaneService, *atomic.Pointer[config.RuntimeConfig]) {
	t.Helper()

	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(
		filepath.Join(root, "state"),
		filepath.Join(root, "cache"),
	)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	runtimeCfg.Store(config.NewDefaultRuntimeConfig())

	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 32,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"cloudflare.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
	})
	scheduler := topology.NewSubscriptionScheduler(topology.SchedulerConfig{
		SubManager: subMgr,
		Pool:       pool,
		Fetcher: func(string) ([]byte, error) {
			return nil, errors.New("test fetcher failure")
		},
	})
	geoSvc := geoip.NewService(geoip.ServiceConfig{
		CacheDir: filepath.Join(root, "geoip"),
		OpenDB:   geoip.NoOpOpen,
	})

	cp := &service.ControlPlaneService{
		Engine:         engine,
		Pool:           pool,
		SubMgr:         subMgr,
		Scheduler:      scheduler,
		Router:         router,
		GeoIP:          geoSvc,
		MatcherRuntime: proxy.NewAccountMatcherRuntime(nil),
		RuntimeCfg:     runtimeCfg,
		EnvCfg: &config.EnvConfig{
			DefaultPlatformStickyTTL:              30 * time.Minute,
			DefaultPlatformRegexFilters:           []string{},
			DefaultPlatformRegionFilters:          []string{},
			DefaultPlatformReverseProxyMissAction: "RANDOM",
			DefaultPlatformAllocationPolicy:       "BALANCED",
		},
	}

	systemInfo := service.SystemInfo{
		Version:   "1.0.0-test",
		GitCommit: "abc123",
		BuildTime: "2026-01-01T00:00:00Z",
		StartedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	srv := NewServer(0, testAdminToken, systemInfo, runtimeCfg, cp.EnvCfg, cp, apiMaxBodyBytes, nil, nil)
	return srv, cp, runtimeCfg
}

func doJSONRequest(t *testing.T, srv *Server, method, path string, body any, authed bool) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody []byte
	var err error
	if body != nil {
		switch v := body.(type) {
		case []byte:
			reqBody = v
		case string:
			reqBody = []byte(v)
		default:
			reqBody, err = json.Marshal(v)
			if err != nil {
				t.Fatalf("marshal request body: %v", err)
			}
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if authed {
		req.Header.Set("Authorization", "Bearer "+testAdminToken)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeJSONMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal body: %v body=%q", err, rec.Body.String())
	}
	return m
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	var er ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
		t.Fatalf("unmarshal error response: %v body=%q", err, rec.Body.String())
	}
	if er.Error.Code != code {
		t.Fatalf("error code: got %q, want %q (body=%s)", er.Error.Code, code, rec.Body.String())
	}
}

func mustCreatePlatform(t *testing.T, srv *Server, name string) string {
	t.Helper()

	rec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/platforms", map[string]any{
		"name": name,
	}, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create platform status: got %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	body := decodeJSONMap(t, rec)
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("create platform missing id: body=%s", rec.Body.String())
	}
	return id
}

type contractRuntimeStats struct {
	platformID string
}

func (contractRuntimeStats) TotalNodes() int    { return 20 }
func (contractRuntimeStats) HealthyNodes() int  { return 15 }
func (contractRuntimeStats) EgressIPCount() int { return 6 }

func (s contractRuntimeStats) LeaseCountsByPlatform() map[string]int {
	return map[string]int{s.platformID: 0}
}

func (s contractRuntimeStats) RoutableNodeCount(platformID string) (int, bool) {
	if platformID != s.platformID {
		return 0, false
	}
	return 8, true
}

func (s contractRuntimeStats) PlatformEgressIPCount(platformID string) (int, bool) {
	if platformID != s.platformID {
		return 0, false
	}
	return 3, true
}

func (p contractRuntimeStats) CollectNodeEWMAs(platformID string) []float64 {
	if platformID == "" {
		return []float64{50, 150, 280}
	}
	if platformID == p.platformID {
		return []float64{80, 120}
	}
	return nil
}

func newObservabilityTestServer(t *testing.T) (*Server, *requestlog.Repo, *metrics.Manager, string) {
	t.Helper()

	root := t.TempDir()
	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	runtimeCfg.Store(config.NewDefaultRuntimeConfig())

	systemInfo := service.SystemInfo{
		Version:   "1.0.0-test",
		GitCommit: "abc123",
		BuildTime: "2026-01-01T00:00:00Z",
		StartedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}

	requestlogRepo := requestlog.NewRepo(root, 64*1024*1024, 2)
	if err := requestlogRepo.Open(); err != nil {
		t.Fatalf("requestlogRepo.Open: %v", err)
	}
	t.Cleanup(func() { _ = requestlogRepo.Close() })

	metricsRepo, err := metrics.NewMetricsRepo(filepath.Join(root, "metrics.db"))
	if err != nil {
		t.Fatalf("metrics.NewMetricsRepo: %v", err)
	}
	t.Cleanup(func() { _ = metricsRepo.Close() })

	platformID := "platform-1"
	metricsManager := metrics.NewManager(metrics.ManagerConfig{
		Repo:                        metricsRepo,
		LatencyBinMs:                100,
		LatencyOverflowMs:           3000,
		BucketSeconds:               300,
		ThroughputRealtimeCapacity:  16,
		ThroughputIntervalSec:       1,
		ConnectionsRealtimeCapacity: 16,
		ConnectionsIntervalSec:      5,
		LeasesRealtimeCapacity:      16,
		LeasesIntervalSec:           5,
		RuntimeStats:                contractRuntimeStats{platformID: platformID},
	})

	srv := NewServer(0, testAdminToken, systemInfo, runtimeCfg, nil, nil, 1<<20, requestlogRepo, metricsManager)
	return srv, requestlogRepo, metricsManager, platformID
}

func seedObservabilityData(
	t *testing.T,
	requestlogRepo *requestlog.Repo,
	metricsManager *metrics.Manager,
	platformID string,
) string {
	t.Helper()

	logID := "log-contract-1"
	inserted, err := requestlogRepo.InsertBatch([]proxy.RequestLogEntry{
		{
			ID:                   logID,
			StartedAtNs:          time.Now().Add(-2 * time.Minute).UnixNano(),
			ProxyType:            proxy.ProxyTypeReverse,
			ClientIP:             "127.0.0.1",
			PlatformID:           platformID,
			PlatformName:         "Platform One",
			Account:              "acct-1",
			TargetHost:           "example.com",
			TargetURL:            "https://example.com/api",
			NodeHash:             "node-1",
			NodeTag:              "sub/tag-1",
			EgressIP:             "8.8.8.8",
			DurationNs:           int64(45 * time.Millisecond),
			NetOK:                true,
			HTTPMethod:           "GET",
			HTTPStatus:           200,
			IngressBytes:         210,
			EgressBytes:          120,
			ReqHeadersLen:        10,
			ReqBodyLen:           11,
			RespHeadersLen:       12,
			RespBodyLen:          13,
			ReqHeadersTruncated:  true,
			ReqBodyTruncated:     true,
			RespHeadersTruncated: false,
			RespBodyTruncated:    true,
			ReqHeaders:           []byte("req-h-1"),
			ReqBody:              []byte("req-b-1"),
			RespHeaders:          []byte("resp-h-1"),
			RespBody:             []byte("resp-b-1"),
		},
		{
			ID:          "log-contract-2",
			StartedAtNs: time.Now().Add(-time.Minute).UnixNano(),
			ProxyType:   proxy.ProxyTypeForward,
			ClientIP:    "127.0.0.2",
			PlatformID:  platformID,
			Account:     "acct-2",
			TargetHost:  "example.org",
			TargetURL:   "https://example.org/resource",
			DurationNs:  int64(12 * time.Millisecond),
			NetOK:       false,
			HTTPMethod:  "POST",
			HTTPStatus:  502,
		},
	})
	if err != nil {
		t.Fatalf("requestlogRepo.InsertBatch: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("inserted: got %d, want %d", inserted, 2)
	}

	now := time.Now()
	metricsManager.ThroughputRing().Push(metrics.RealtimeSample{
		Timestamp:  now.Add(-30 * time.Second),
		IngressBPS: 123,
		EgressBPS:  456,
	})
	metricsManager.ConnectionsRing().Push(metrics.RealtimeSample{
		Timestamp:     now.Add(-30 * time.Second),
		InboundConns:  7,
		OutboundConns: 5,
	})
	metricsManager.LeasesRing().Push(metrics.RealtimeSample{
		Timestamp:        now.Add(-30 * time.Second),
		LeasesByPlatform: map[string]int{platformID: 3},
	})
	metricsManager.OnTrafficDelta(1000, 1500)
	metricsManager.OnRequestFinished(proxy.RequestFinishedEvent{
		PlatformID: platformID,
		ProxyType:  proxy.ProxyTypeForward,
		NetOK:      true,
		DurationNs: int64(120 * time.Millisecond),
	})
	metricsManager.OnRequestFinished(proxy.RequestFinishedEvent{
		PlatformID: platformID,
		ProxyType:  proxy.ProxyTypeForward,
		NetOK:      false,
		DurationNs: int64(240 * time.Millisecond),
	})
	metricsManager.OnProbeEvent(metrics.ProbeEvent{Kind: metrics.ProbeKindEgress})
	metricsManager.OnLeaseEvent(metrics.LeaseMetricEvent{
		PlatformID: platformID,
		Op:         metrics.LeaseOpRemove,
		LifetimeNs: int64(30 * time.Second),
	})
	metricsManager.Stop() // ForceFlush bucket data without starting background loops.

	trafficRows, err := metricsManager.Repo().QueryTraffic(0, time.Now().Add(time.Hour).Unix())
	if err != nil {
		t.Fatalf("metrics QueryTraffic: %v", err)
	}
	if len(trafficRows) == 0 {
		t.Fatal("expected flushed traffic rows")
	}
	bucketStart := trafficRows[0].BucketStartUnix

	if err := metricsManager.Repo().WriteNodePoolSnapshot(bucketStart, 20, 15, 6); err != nil {
		t.Fatalf("WriteNodePoolSnapshot: %v", err)
	}
	if err := metricsManager.Repo().WriteLatencyBucket(bucketStart, "", []int64{1, 2, 3}); err != nil {
		t.Fatalf("WriteLatencyBucket global: %v", err)
	}
	if err := metricsManager.Repo().WriteLatencyBucket(bucketStart, platformID, []int64{2, 1, 0}); err != nil {
		t.Fatalf("WriteLatencyBucket platform: %v", err)
	}

	return logID
}

func TestAPIContract_HealthzAndAuth(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodGet, "/healthz", nil, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status: got %d, want %d", rec.Code, http.StatusOK)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/platforms", nil, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	assertErrorCode(t, rec, "UNAUTHORIZED")
}

func TestAPIContract_RequestBodyTooLarge(t *testing.T) {
	srv, _, _ := newControlPlaneTestServerWithBodyLimit(t, 64)

	largeName := strings.Repeat("a", 256)
	rec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/platforms", map[string]any{
		"name": largeName,
	}, true)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	assertErrorCode(t, rec, "PAYLOAD_TOO_LARGE")
}

func TestAPIContract_GetLease_AccountPathEncoding(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)

	platformID := mustCreatePlatform(t, srv, "lease-account-encoding")
	account := "team%2Fa"
	hash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"1.1.1.1","port":443}`))
	now := time.Now().UnixNano()
	cp.Router.RestoreLeases([]model.Lease{
		{
			PlatformID:     platformID,
			Account:        account,
			NodeHash:       hash.Hex(),
			EgressIP:       "1.2.3.4",
			ExpiryNs:       now + int64(time.Hour),
			LastAccessedNs: now,
		},
	})

	// Encode "%" as %25 in the path so one decode pass yields the literal account "team%2Fa".
	rec := doJSONRequest(
		t,
		srv,
		http.MethodGet,
		"/api/v1/platforms/"+platformID+"/leases/team%252Fa",
		nil,
		true,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSONMap(t, rec)
	if body["account"] != account {
		t.Fatalf("account: got %v, want %q", body["account"], account)
	}
}

func TestAPIContract_PaginationAndSorting(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	_ = mustCreatePlatform(t, srv, "zeta")
	_ = mustCreatePlatform(t, srv, "alpha")
	_ = mustCreatePlatform(t, srv, "beta")

	rec := doJSONRequest(
		t,
		srv,
		http.MethodGet,
		"/api/v1/platforms?sort_by=name&sort_order=asc&limit=2&offset=1",
		nil,
		true,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("list platforms status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSONMap(t, rec)
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items type: got %T", body["items"])
	}
	if len(items) != 2 {
		t.Fatalf("items len: got %d, want %d", len(items), 2)
	}

	item0 := items[0].(map[string]any)
	item1 := items[1].(map[string]any)
	if item0["name"] != "beta" || item1["name"] != "zeta" {
		t.Fatalf("unexpected order: got [%v, %v]", item0["name"], item1["name"])
	}
}

func TestAPIContract_PlatformStickyTTLMustBePositive(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	createCases := []struct {
		name      string
		stickyTTL string
	}{
		{name: "zero", stickyTTL: "0s"},
		{name: "negative", stickyTTL: "-1s"},
	}
	for _, tc := range createCases {
		t.Run("create_"+tc.name, func(t *testing.T) {
			rec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/platforms", map[string]any{
				"name":       "sticky-create-" + tc.name,
				"sticky_ttl": tc.stickyTTL,
			}, true)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("create status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			assertErrorCode(t, rec, "INVALID_ARGUMENT")
		})
	}

	platformID := mustCreatePlatform(t, srv, "sticky-patch-target")
	for _, tc := range createCases {
		t.Run("patch_"+tc.name, func(t *testing.T) {
			rec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/platforms/"+platformID, map[string]any{
				"sticky_ttl": tc.stickyTTL,
			}, true)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("patch status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			assertErrorCode(t, rec, "INVALID_ARGUMENT")
		})
	}
}

func TestAPIContract_SystemConfigPatchSemantics(t *testing.T) {
	srv, _, runtimeCfg := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/system/config", map[string]any{
		"request_log_enabled":                     true,
		"reverse_proxy_log_req_headers_max_bytes": 2048,
		"p2c_latency_window":                      "7m",
		"cache_flush_interval":                    "30s",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch config status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSONMap(t, rec)
	if body["request_log_enabled"] != true {
		t.Fatalf("request_log_enabled: got %v, want true", body["request_log_enabled"])
	}
	if body["reverse_proxy_log_req_headers_max_bytes"] != float64(2048) {
		t.Fatalf("reverse_proxy_log_req_headers_max_bytes: got %v", body["reverse_proxy_log_req_headers_max_bytes"])
	}
	if body["p2c_latency_window"] != "7m0s" {
		t.Fatalf("p2c_latency_window: got %v, want 7m0s", body["p2c_latency_window"])
	}
	if body["cache_flush_interval"] != "30s" {
		t.Fatalf("cache_flush_interval: got %v, want 30s", body["cache_flush_interval"])
	}

	snap := runtimeCfg.Load()
	if !snap.RequestLogEnabled {
		t.Fatal("runtime pointer did not reflect patched request_log_enabled")
	}
	if snap.ReverseProxyLogReqHeadersMaxBytes != 2048 {
		t.Fatalf("runtime pointer reverse_proxy_log_req_headers_max_bytes=%d, want 2048", snap.ReverseProxyLogReqHeadersMaxBytes)
	}

	cases := []struct {
		name string
		body any
	}{
		{name: "empty patch", body: map[string]any{}},
		{name: "unknown field", body: map[string]any{"unknown_field": 1}},
		{name: "null value", body: map[string]any{"request_log_enabled": nil}},
		{name: "empty latency_test_url", body: map[string]any{"latency_test_url": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/system/config", tc.body, true)
			if r.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want %d, body=%s", r.Code, http.StatusBadRequest, r.Body.String())
			}
			assertErrorCode(t, r, "INVALID_ARGUMENT")
		})
	}
}

func TestAPIContract_SystemDefaultConfigSnapshot(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	patchRec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/system/config", map[string]any{
		"user_agent":               "custom-agent",
		"request_log_enabled":      true,
		"max_consecutive_failures": 9,
	}, true)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch config status: got %d, want %d, body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}

	defaultRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/system/config/default", nil, true)
	if defaultRec.Code != http.StatusOK {
		t.Fatalf("default config status: got %d, want %d, body=%s", defaultRec.Code, http.StatusOK, defaultRec.Body.String())
	}
	defaultBody := decodeJSONMap(t, defaultRec)
	if defaultBody["user_agent"] != "sing-box" {
		t.Fatalf("default user_agent: got %v, want sing-box", defaultBody["user_agent"])
	}
	if defaultBody["request_log_enabled"] != false {
		t.Fatalf("default request_log_enabled: got %v, want false", defaultBody["request_log_enabled"])
	}
	if defaultBody["max_consecutive_failures"] != float64(3) {
		t.Fatalf("default max_consecutive_failures: got %v, want 3", defaultBody["max_consecutive_failures"])
	}

	currentRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/system/config", nil, true)
	if currentRec.Code != http.StatusOK {
		t.Fatalf("current config status: got %d, want %d, body=%s", currentRec.Code, http.StatusOK, currentRec.Body.String())
	}
	currentBody := decodeJSONMap(t, currentRec)
	if currentBody["user_agent"] != "custom-agent" {
		t.Fatalf("current user_agent: got %v, want custom-agent", currentBody["user_agent"])
	}
	if currentBody["request_log_enabled"] != true {
		t.Fatalf("current request_log_enabled: got %v, want true", currentBody["request_log_enabled"])
	}
	if currentBody["max_consecutive_failures"] != float64(9) {
		t.Fatalf("current max_consecutive_failures: got %v, want 9", currentBody["max_consecutive_failures"])
	}
}

func TestAPIContract_SystemEnvConfigSnapshot(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/system/config/env", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("env config status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeJSONMap(t, rec)
	if body["default_platform_sticky_ttl"] != cp.EnvCfg.DefaultPlatformStickyTTL.String() {
		t.Fatalf(
			"default_platform_sticky_ttl: got %v, want %s",
			body["default_platform_sticky_ttl"],
			cp.EnvCfg.DefaultPlatformStickyTTL.String(),
		)
	}
	if body["default_platform_reverse_proxy_miss_action"] != "RANDOM" {
		t.Fatalf(
			"default_platform_reverse_proxy_miss_action: got %v, want RANDOM",
			body["default_platform_reverse_proxy_miss_action"],
		)
	}
	if body["default_platform_allocation_policy"] != "BALANCED" {
		t.Fatalf(
			"default_platform_allocation_policy: got %v, want BALANCED",
			body["default_platform_allocation_policy"],
		)
	}
	if body["admin_token_set"] != false {
		t.Fatalf("admin_token_set: got %v, want false", body["admin_token_set"])
	}
	if body["proxy_token_set"] != false {
		t.Fatalf("proxy_token_set: got %v, want false", body["proxy_token_set"])
	}
	if _, ok := body["admin_token"]; ok {
		t.Fatalf("admin_token should not be exposed: body=%s", rec.Body.String())
	}
	if _, ok := body["proxy_token"]; ok {
		t.Fatalf("proxy_token should not be exposed: body=%s", rec.Body.String())
	}
}

func TestAPIContract_ModuleAndActionEndpoints(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	// platform + leases
	platformID := mustCreatePlatform(t, srv, "lease-plat")

	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/platforms/"+platformID+"/leases", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("list leases status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeJSONMap(t, rec)
	if _, ok := body["items"]; !ok {
		t.Fatalf("list leases missing items: body=%s", rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodDelete, "/api/v1/platforms/not-a-uuid/leases", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid platform_id status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/platforms/not-a-uuid", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("platform invalid id status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/platforms/preview-filter", map[string]any{
		"platform_id": "not-a-uuid",
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("preview-filter invalid platform_id status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/platforms/"+platformID+"/actions/rebuild-routable-view", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("rebuild action status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// subscriptions
	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "sub-a",
		"url":  "https://example.com/sub",
	}, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create subscription status: got %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions/11111111-1111-1111-1111-111111111111/actions/refresh", nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("refresh missing subscription status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/not-a-uuid", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("subscription invalid id status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	// account header rules
	rec = doJSONRequest(t, srv, http.MethodPut, "/api/v1/account-header-rules/api.example.com%2Fv1", map[string]any{
		"headers": []string{"Authorization"},
	}, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upsert rule status: got %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodPut, "/api/v1/account-header-rules/api.example.com%2Fv1", map[string]any{
		"headers": []string{"x-api-key"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("update existing rule status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/account-header-rules:resolve", map[string]any{
		"url": "https://api.example.com/v1/orders/1",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve rule status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	resolved := decodeJSONMap(t, rec)
	if resolved["matched_url_prefix"] != "api.example.com/v1" {
		t.Fatalf("matched_url_prefix: got %v, want api.example.com/v1", resolved["matched_url_prefix"])
	}

	// nodes (invalid hash should be 400 before probe manager is needed)
	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/nodes/not-hex/actions/probe-egress", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("probe-egress invalid hash status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/nodes/not-hex/actions/probe-latency", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("probe-latency invalid hash status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?platform_id=not-a-uuid", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("nodes invalid platform_id status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?subscription_id=not-a-uuid", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("nodes invalid subscription_id status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	// geoip
	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/geoip/status", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("geoip status code: got %d, want %d", rec.Code, http.StatusOK)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/geoip/lookup", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("geoip lookup missing ip status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/geoip/lookup", map[string]any{
		"ips": []string{"1.2.3.4", "not-an-ip"},
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("geoip batch invalid ip status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
	var geoBatchErr ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &geoBatchErr); err != nil {
		t.Fatalf("unmarshal geoip batch invalid response: %v body=%q", err, rec.Body.String())
	}
	if !strings.Contains(geoBatchErr.Error.Message, "ips[1]") {
		t.Fatalf("geoip batch invalid message: got %q, want contains %q", geoBatchErr.Error.Message, "ips[1]")
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/geoip/lookup", map[string]any{
		"ips": []string{"1.2.3.4", "8.8.8.8"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("geoip batch success status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body = decodeJSONMap(t, rec)
	geoResultsRaw, ok := body["results"]
	if !ok {
		t.Fatalf("geoip batch missing results: body=%s", rec.Body.String())
	}
	geoResults, ok := geoResultsRaw.([]any)
	if !ok {
		t.Fatalf("geoip batch results type: got %T, want []any", geoResultsRaw)
	}
	if len(geoResults) != 2 {
		t.Fatalf("geoip batch results len: got %d, want %d", len(geoResults), 2)
	}

	// downloader is intentionally nil in test harness, so update-now returns INTERNAL.
	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/geoip/actions/update-now", nil, true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("geoip update-now status: got %d, want %d, body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	assertErrorCode(t, rec, "INTERNAL")
}

func TestAPIContract_SubscriptionUpdateIntervalMinimum(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":            "too-fast",
		"url":             "https://example.com/sub-fast",
		"update_interval": "10s",
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create subscription invalid interval status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "normal-sub",
		"url":  "https://example.com/sub-normal",
	}, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create subscription status: got %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	body := decodeJSONMap(t, rec)
	subID, _ := body["id"].(string)
	if subID == "" {
		t.Fatalf("create subscription missing id: body=%s", rec.Body.String())
	}

	rec = doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subID, map[string]any{
		"update_interval": "5s",
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch subscription invalid interval status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

func TestAPIContract_PreviewFilterUsesPaginationEnvelope(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(
		t,
		srv,
		http.MethodPost,
		"/api/v1/platforms/preview-filter?limit=5&offset=1",
		map[string]any{
			"platform_spec": map[string]any{
				"regex_filters":  []string{},
				"region_filters": []string{},
			},
		},
		true,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview-filter status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSONMap(t, rec)
	itemsRaw, ok := body["items"]
	if !ok {
		t.Fatalf("preview-filter missing items: body=%s", rec.Body.String())
	}
	items, ok := itemsRaw.([]any)
	if !ok {
		t.Fatalf("preview-filter items type: got %T, want []any", itemsRaw)
	}
	if len(items) != 0 {
		t.Fatalf("preview-filter expected empty items, got %d", len(items))
	}
	if _, ok := body["nodes"]; ok {
		t.Fatalf("preview-filter should not return legacy nodes field: body=%s", rec.Body.String())
	}
}

func TestAPIContract_DeleteRuleRejectsInvalidPrefix(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodDelete, "/api/v1/account-header-rules/api.example.com%3Fq%3D1", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete rule invalid prefix status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

func TestAPIContract_UpsertRuleRequiresPathPrefix(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodPut, "/api/v1/account-header-rules/", map[string]any{
		"url_prefix": "api.example.com/v1",
		"headers":    []string{"Authorization"},
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("upsert rule missing path prefix status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

func TestAPIContract_RequestLogEndpoints(t *testing.T) {
	srv, requestlogRepo, metricsManager, platformID := newObservabilityTestServer(t)
	logID := seedObservabilityData(t, requestlogRepo, metricsManager, platformID)

	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/request-logs?platform_id="+platformID+"&limit=1", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("list request logs status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeJSONMap(t, rec)
	if body["limit"] != float64(1) {
		t.Fatalf("list limit: got %v, want 1", body["limit"])
	}
	if body["has_more"] != true {
		t.Fatalf("list has_more: got %v, want true", body["has_more"])
	}
	nextCursor, ok := body["next_cursor"].(string)
	if !ok || nextCursor == "" {
		t.Fatalf("list next_cursor: got %T value=%v, want non-empty string", body["next_cursor"], body["next_cursor"])
	}
	itemsRaw, ok := body["items"]
	if !ok {
		t.Fatalf("missing items field: body=%s", rec.Body.String())
	}
	items, ok := itemsRaw.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items: got %T len=%d, want 1", itemsRaw, len(items))
	}

	rec = doJSONRequest(
		t,
		srv,
		http.MethodGet,
		"/api/v1/request-logs?platform_id="+platformID+"&limit=1&cursor="+url.QueryEscape(nextCursor),
		nil,
		true,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("list request logs cursor status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body = decodeJSONMap(t, rec)
	if body["has_more"] != false {
		t.Fatalf("cursor page has_more: got %v, want false", body["has_more"])
	}
	itemsRaw, ok = body["items"]
	if !ok {
		t.Fatalf("cursor page missing items field: body=%s", rec.Body.String())
	}
	items, ok = itemsRaw.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("cursor page items: got %T len=%d, want 1", itemsRaw, len(items))
	}

	rec = doJSONRequest(
		t,
		srv,
		http.MethodGet,
		"/api/v1/request-logs?platform_name="+url.QueryEscape("Platform One")+"&limit=20",
		nil,
		true,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("list request logs by platform_name status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body = decodeJSONMap(t, rec)
	itemsRaw, ok = body["items"]
	if !ok {
		t.Fatalf("platform_name filter missing items field: body=%s", rec.Body.String())
	}
	items, ok = itemsRaw.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("platform_name filter items: got %T len=%d, want 1", itemsRaw, len(items))
	}
	rowMap, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("platform_name filter first item type: got %T", items[0])
	}
	if rowMap["platform_name"] != "Platform One" {
		t.Fatalf("platform_name filter first item platform_name: got %v, want %q", rowMap["platform_name"], "Platform One")
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/request-logs/"+logID, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("get request log status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	row := decodeJSONMap(t, rec)
	if row["id"] != logID {
		t.Fatalf("id: got %v, want %q", row["id"], logID)
	}
	if row["payload_present"] != true {
		t.Fatalf("payload_present: got %v, want true", row["payload_present"])
	}
	if row["ingress_bytes"] != float64(210) || row["egress_bytes"] != float64(120) {
		t.Fatalf("traffic bytes mismatch: ingress=%v egress=%v", row["ingress_bytes"], row["egress_bytes"])
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/request-logs/"+logID+"/payloads", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("get request log payload status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	payload := decodeJSONMap(t, rec)
	if payload["req_headers_b64"] != base64.StdEncoding.EncodeToString([]byte("req-h-1")) {
		t.Fatalf("req_headers_b64 mismatch: got %v", payload["req_headers_b64"])
	}
	if payload["resp_body_b64"] != base64.StdEncoding.EncodeToString([]byte("resp-b-1")) {
		t.Fatalf("resp_body_b64 mismatch: got %v", payload["resp_body_b64"])
	}
	trunc, ok := payload["truncated"].(map[string]any)
	if !ok {
		t.Fatalf("truncated type: got %T", payload["truncated"])
	}
	if trunc["req_headers"] != true || trunc["req_body"] != true || trunc["resp_body"] != true {
		t.Fatalf("truncated flags mismatch: %+v", trunc)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/request-logs?from=not-a-time", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid from status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	invalidCases := []string{
		"/api/v1/request-logs?limit=bad",
		"/api/v1/request-logs?limit=100001",
		"/api/v1/request-logs?offset=1",
		"/api/v1/request-logs?cursor=not-base64",
		"/api/v1/request-logs?proxy_type=3",
		"/api/v1/request-logs?net_ok=2",
		"/api/v1/request-logs?net_ok=1",
		"/api/v1/request-logs?net_ok=maybe",
		"/api/v1/request-logs?http_status=bad",
		"/api/v1/request-logs?http_status=99",
	}
	for _, path := range invalidCases {
		rec = doJSONRequest(t, srv, http.MethodGet, path, nil, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status: got %d, want %d, body=%s", path, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		assertErrorCode(t, rec, "INVALID_ARGUMENT")
	}
}

func TestAPIContract_MetricsEndpoints(t *testing.T) {
	srv, requestlogRepo, metricsManager, platformID := newObservabilityTestServer(t)
	_ = seedObservabilityData(t, requestlogRepo, metricsManager, platformID)

	from := url.QueryEscape(time.Unix(0, 0).UTC().Format(time.RFC3339Nano))
	to := url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))

	checkItemsEndpoint := func(path string) {
		t.Helper()
		rec := doJSONRequest(t, srv, http.MethodGet, path, nil, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status: got %d, want %d, body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
		}
		body := decodeJSONMap(t, rec)
		items, ok := body["items"].([]any)
		if !ok || len(items) == 0 {
			t.Fatalf("%s items: got %T len=%d", path, body["items"], len(items))
		}
	}

	checkRealtimeEndpoint := func(path string, wantStep float64) {
		t.Helper()
		rec := doJSONRequest(t, srv, http.MethodGet, path, nil, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status: got %d, want %d, body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
		}
		body := decodeJSONMap(t, rec)
		if body["step_seconds"] != wantStep {
			t.Fatalf("%s step_seconds: got %v, want %v", path, body["step_seconds"], wantStep)
		}
		items, ok := body["items"].([]any)
		if !ok || len(items) == 0 {
			t.Fatalf("%s items: got %T len=%d", path, body["items"], len(items))
		}
	}

	checkRealtimeEndpoint("/api/v1/metrics/realtime/throughput?from="+from+"&to="+to, 1)
	checkRealtimeEndpoint("/api/v1/metrics/realtime/connections?from="+from+"&to="+to, 5)
	checkRealtimeEndpoint("/api/v1/metrics/realtime/leases?platform_id="+platformID+"&from="+from+"&to="+to, 5)
	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/metrics/realtime/leases?from="+from+"&to="+to, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("global realtime leases status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeJSONMap(t, rec)
	if body["platform_id"] != "" {
		t.Fatalf("global realtime leases platform_id: got %v, want empty", body["platform_id"])
	}
	if items, ok := body["items"].([]any); !ok || len(items) != 1 {
		t.Fatalf("global realtime leases items: got %T len=%d, want len=1", body["items"], len(items))
	} else if item, ok := items[0].(map[string]any); !ok || item["active_leases"] != float64(3) {
		t.Fatalf("global realtime leases active_leases: got %+v, want 3", items[0])
	}
	checkItemsEndpoint("/api/v1/metrics/history/traffic?from=" + from + "&to=" + to)
	checkItemsEndpoint("/api/v1/metrics/history/requests?platform_id=" + platformID + "&from=" + from + "&to=" + to)
	checkItemsEndpoint("/api/v1/metrics/history/access-latency?platform_id=" + platformID + "&from=" + from + "&to=" + to)
	checkItemsEndpoint("/api/v1/metrics/history/probes?from=" + from + "&to=" + to)
	checkItemsEndpoint("/api/v1/metrics/history/node-pool?from=" + from + "&to=" + to)
	checkItemsEndpoint("/api/v1/metrics/history/lease-lifetime?platform_id=" + platformID + "&from=" + from + "&to=" + to)

	// Without platform_id, mixed global+platform rows must not leak through.
	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/metrics/history/traffic?from="+from+"&to="+to, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("global traffic status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body = decodeJSONMap(t, rec)
	if items, ok := body["items"].([]any); !ok || len(items) != 1 {
		t.Fatalf("global traffic items: got %T len=%d, want len=1", body["items"], len(items))
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/metrics/history/requests?from="+from+"&to="+to, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("global requests status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body = decodeJSONMap(t, rec)
	if items, ok := body["items"].([]any); !ok || len(items) != 1 {
		t.Fatalf("global requests items: got %T len=%d, want len=1", body["items"], len(items))
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/metrics/history/access-latency?from="+from+"&to="+to, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("global access-latency status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body = decodeJSONMap(t, rec)
	if items, ok := body["items"].([]any); !ok || len(items) != 1 {
		t.Fatalf("global access-latency items: got %T len=%d, want len=1", body["items"], len(items))
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/metrics/snapshots/node-pool", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot node-pool status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body = decodeJSONMap(t, rec)
	if body["total_nodes"] != float64(20) || body["healthy_nodes"] != float64(15) {
		t.Fatalf("snapshot node-pool values mismatch: %+v", body)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/metrics/snapshots/platform-node-pool?platform_id="+platformID, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot platform-node-pool status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body = decodeJSONMap(t, rec)
	if body["platform_id"] != platformID || body["routable_node_count"] != float64(8) {
		t.Fatalf("snapshot platform-node-pool values mismatch: %+v", body)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/metrics/snapshots/node-latency-distribution?platform_id="+platformID, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot node-latency-distribution status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body = decodeJSONMap(t, rec)
	if body["scope"] != "platform" || body["sample_count"] == float64(0) {
		t.Fatalf("snapshot node-latency-distribution values mismatch: %+v", body)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/metrics/history/traffic?from=bad-time", nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid metrics from status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}
