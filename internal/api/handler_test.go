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

	svc := service.NewMemorySystemService(
		service.SystemInfo{
			Version:   "1.0.0-test",
			GitCommit: "abc123",
			BuildTime: "2026-01-01T00:00:00Z",
			StartedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		runtimeCfg,
	)
	return NewServer(0, "test-admin-token", svc)
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
