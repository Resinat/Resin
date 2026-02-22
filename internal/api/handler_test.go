package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/service"
)

func newTestServer() *Server {
	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	runtimeCfg.Store(config.NewDefaultRuntimeConfig())
	envCfg := &config.EnvConfig{
		CacheDir:                              "/tmp/resin/cache",
		StateDir:                              "/tmp/resin/state",
		LogDir:                                "/tmp/resin/log",
		ListenAddress:                         "127.0.0.1",
		APIPort:                               2620,
		ForwardProxyPort:                      2621,
		ReverseProxyPort:                      2622,
		APIMaxBodyBytes:                       1 << 20,
		MaxLatencyTableEntries:                128,
		ProbeConcurrency:                      1000,
		GeoIPUpdateSchedule:                   "0 7 * * *",
		DefaultPlatformStickyTTL:              7 * 24 * time.Hour,
		DefaultPlatformRegexFilters:           []string{"^Provider/.*"},
		DefaultPlatformRegionFilters:          []string{"us", "hk"},
		DefaultPlatformReverseProxyMissAction: "RANDOM",
		DefaultPlatformAllocationPolicy:       "BALANCED",
		ProbeTimeout:                          15 * time.Second,
		ResourceFetchTimeout:                  30 * time.Second,
		ProxyTransportMaxIdleConns:            1024,
		ProxyTransportMaxIdleConnsPerHost:     64,
		ProxyTransportIdleConnTimeout:         90 * time.Second,
		RequestLogQueueSize:                   8192,
		RequestLogQueueFlushBatchSize:         4096,
		RequestLogQueueFlushInterval:          5 * time.Minute,
		RequestLogDBMaxMB:                     512,
		RequestLogDBRetainCount:               5,
		AdminToken:                            "test-admin-token",
		ProxyToken:                            "test-proxy-token",
		MetricThroughputIntervalSeconds:       1,
		MetricThroughputRetentionSeconds:      3600,
		MetricBucketSeconds:                   3600,
		MetricConnectionsIntervalSeconds:      5,
		MetricConnectionsRetentionSeconds:     18000,
		MetricLeasesIntervalSeconds:           5,
		MetricLeasesRetentionSeconds:          18000,
		MetricLatencyBinWidthMS:               100,
		MetricLatencyBinOverflowMS:            3000,
	}

	systemInfo := service.SystemInfo{
		Version:   "1.0.0-test",
		GitCommit: "abc123",
		BuildTime: "2026-01-01T00:00:00Z",
		StartedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	return NewServer(0, "test-admin-token", systemInfo, runtimeCfg, envCfg, nil, 1<<20, nil, nil)
}

// --- /healthz ---

func TestHealthz_OK(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field: got %q, want %q", body["status"], "ok")
	}
}

func TestHealthz_NoAuth(t *testing.T) {
	// healthz should succeed WITHOUT any auth header
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("healthz should not require auth, got status %d", rec.Code)
	}
}

// --- /api/v1/system/info ---

func TestSystemInfo_OK(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	req.Header.Set("Authorization", "Bearer test-admin-token")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if body["version"] != "1.0.0-test" {
		t.Errorf("version: got %q, want %q", body["version"], "1.0.0-test")
	}
	if body["git_commit"] != "abc123" {
		t.Errorf("git_commit: got %q, want %q", body["git_commit"], "abc123")
	}
	if _, ok := body["started_at"]; !ok {
		t.Error("missing started_at field")
	}
}

func TestSystemInfo_RequiresAuth(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// --- /api/v1/system/config ---

func TestSystemConfig_OK(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config", nil)
	req.Header.Set("Authorization", "Bearer test-admin-token")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Check some default values
	if body["user_agent"] != "sing-box" {
		t.Errorf("user_agent: got %q, want %q", body["user_agent"], "sing-box")
	}
	if body["request_log_enabled"] != false {
		t.Errorf("request_log_enabled: got %v, want false", body["request_log_enabled"])
	}

	// JSON numbers are float64
	if maxFail, ok := body["max_consecutive_failures"].(float64); !ok || maxFail != 3 {
		t.Errorf("max_consecutive_failures: got %v, want 3", body["max_consecutive_failures"])
	}

	if _, ok := body["default_platform_config"]; ok {
		t.Error("default_platform_config should not be exposed in /system/config")
	}
	if _, ok := body["probe_timeout"]; ok {
		t.Error("probe_timeout should not be exposed in /system/config")
	}
	if _, ok := body["resource_fetch_timeout"]; ok {
		t.Error("resource_fetch_timeout should not be exposed in /system/config")
	}
	if _, ok := body["request_log_db_max_mb"]; ok {
		t.Error("request_log_db_max_mb should be env-only and not exposed in /system/config")
	}
	if _, ok := body["request_log_db_retain_count"]; ok {
		t.Error("request_log_db_retain_count should be env-only and not exposed in /system/config")
	}
	if _, ok := body["request_log_batch_size"]; ok {
		t.Error("request_log_batch_size should be env-only and not exposed in /system/config")
	}
	if _, ok := body["metric_latency_bin_ms"]; ok {
		t.Error("metric_latency_bin_ms should be env-only and not exposed in /system/config")
	}
	if _, ok := body["metric_latency_overflow_ms"]; ok {
		t.Error("metric_latency_overflow_ms should be env-only and not exposed in /system/config")
	}
	if _, ok := body["metric_bucket_seconds"]; ok {
		t.Error("metric_bucket_seconds should be env-only and not exposed in /system/config")
	}
	if _, ok := body["metric_realtime_capacity"]; ok {
		t.Error("metric_realtime_capacity should be env-only and not exposed in /system/config")
	}
	if _, ok := body["metric_sample_interval_sec"]; ok {
		t.Error("metric_sample_interval_sec should be env-only and not exposed in /system/config")
	}
}

func TestSystemConfig_RequiresAuth(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// --- /api/v1/system/config/env ---

func TestSystemEnvConfig_OK(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config/env", nil)
	req.Header.Set("Authorization", "Bearer test-admin-token")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if body["cache_dir"] != "/tmp/resin/cache" {
		t.Errorf("cache_dir: got %q, want %q", body["cache_dir"], "/tmp/resin/cache")
	}
	if body["listen_address"] != "127.0.0.1" {
		t.Errorf("listen_address: got %q, want %q", body["listen_address"], "127.0.0.1")
	}
	if body["default_platform_sticky_ttl"] != "168h0m0s" {
		t.Errorf("default_platform_sticky_ttl: got %q, want %q", body["default_platform_sticky_ttl"], "168h0m0s")
	}
	if body["probe_timeout"] != "15s" {
		t.Errorf("probe_timeout: got %q, want %q", body["probe_timeout"], "15s")
	}
	if body["admin_token_set"] != true {
		t.Errorf("admin_token_set: got %v, want true", body["admin_token_set"])
	}
	if body["proxy_token_set"] != true {
		t.Errorf("proxy_token_set: got %v, want true", body["proxy_token_set"])
	}
	if _, ok := body["admin_token"]; ok {
		t.Error("admin_token should not be exposed in /system/config/env")
	}
	if _, ok := body["proxy_token"]; ok {
		t.Error("proxy_token should not be exposed in /system/config/env")
	}
}

func TestSystemEnvConfig_RequiresAuth(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config/env", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// --- /api/v1/system/config/default ---

func TestSystemDefaultConfig_OK(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config/default", nil)
	req.Header.Set("Authorization", "Bearer test-admin-token")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if body["user_agent"] != "sing-box" {
		t.Errorf("user_agent: got %q, want %q", body["user_agent"], "sing-box")
	}
	if body["request_log_enabled"] != false {
		t.Errorf("request_log_enabled: got %v, want false", body["request_log_enabled"])
	}
	if maxFail, ok := body["max_consecutive_failures"].(float64); !ok || maxFail != 3 {
		t.Errorf("max_consecutive_failures: got %v, want 3", body["max_consecutive_failures"])
	}
}

func TestSystemDefaultConfig_RequiresAuth(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config/default", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
