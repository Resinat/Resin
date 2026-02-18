package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/geoip"
	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/proxy"
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

	systemSvc := service.NewMemorySystemService(
		service.SystemInfo{
			Version:   "1.0.0-test",
			GitCommit: "abc123",
			BuildTime: "2026-01-01T00:00:00Z",
			StartedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		runtimeCfg,
	)
	srv := NewServer(0, testAdminToken, systemSvc, cp, apiMaxBodyBytes)
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
