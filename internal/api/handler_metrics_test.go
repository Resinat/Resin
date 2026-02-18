package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/metrics"
)

type testPlatformStats struct {
	platforms map[string]struct{}
}

func (s testPlatformStats) RoutableNodeCount(platformID string) (int, bool) {
	_, ok := s.platforms[platformID]
	return 0, ok
}

func (s testPlatformStats) PlatformEgressIPCount(platformID string) (int, bool) {
	_, ok := s.platforms[platformID]
	return 0, ok
}

type testNodeLatencyProvider struct {
	global   []float64
	platform map[string][]float64
}

func (p testNodeLatencyProvider) CollectNodeEWMAs(platformID string) []float64 {
	if platformID == "" {
		return append([]float64(nil), p.global...)
	}
	if p.platform == nil {
		return nil
	}
	return append([]float64(nil), p.platform[platformID]...)
}

func newTestMetricsManager(t *testing.T, existingPlatforms ...string) *metrics.Manager {
	return newTestMetricsManagerWithNodeLatency(t, testNodeLatencyProvider{}, existingPlatforms...)
}

func newTestMetricsManagerWithNodeLatency(
	t *testing.T,
	provider testNodeLatencyProvider,
	existingPlatforms ...string,
) *metrics.Manager {
	t.Helper()

	repo, err := metrics.NewMetricsRepo(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("NewMetricsRepo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	platforms := make(map[string]struct{}, len(existingPlatforms))
	for _, id := range existingPlatforms {
		platforms[id] = struct{}{}
	}

	return metrics.NewManager(metrics.ManagerConfig{
		Repo:                        repo,
		LatencyBinMs:                100,
		LatencyOverflowMs:           3000,
		BucketSeconds:               3600,
		ThroughputRealtimeCapacity:  16,
		ThroughputIntervalSec:       1,
		ConnectionsRealtimeCapacity: 16,
		ConnectionsIntervalSec:      5,
		LeasesRealtimeCapacity:      16,
		LeasesIntervalSec:           7,
		PlatformStats:               testPlatformStats{platforms: platforms},
		NodeLatency:                 provider,
	})
}

func assertNotFoundError(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if body.Error.Code != "NOT_FOUND" {
		t.Fatalf("error.code: got %q, want %q", body.Error.Code, "NOT_FOUND")
	}
}

func assertInvalidArgumentError(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if body.Error.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error.code: got %q, want %q", body.Error.Code, "INVALID_ARGUMENT")
	}
}

func TestMetricsHandlers_NonexistentPlatformReturnsNotFound(t *testing.T) {
	mgr := newTestMetricsManager(t, "existing-platform")

	cases := []struct {
		name    string
		handler http.Handler
		path    string
	}{
		{
			name:    "realtime leases",
			handler: HandleRealtimeLeases(mgr),
			path:    "/api/v1/metrics/realtime/leases?platform_id=missing-platform",
		},
		{
			name:    "history traffic",
			handler: HandleHistoryTraffic(mgr),
			path:    "/api/v1/metrics/history/traffic?platform_id=missing-platform",
		},
		{
			name:    "history requests",
			handler: HandleHistoryRequests(mgr),
			path:    "/api/v1/metrics/history/requests?platform_id=missing-platform",
		},
		{
			name:    "history access latency",
			handler: HandleHistoryAccessLatency(mgr),
			path:    "/api/v1/metrics/history/access-latency?platform_id=missing-platform",
		},
		{
			name:    "history lease lifetime",
			handler: HandleHistoryLeaseLifetime(mgr),
			path:    "/api/v1/metrics/history/lease-lifetime?platform_id=missing-platform",
		},
		{
			name:    "snapshot node latency distribution",
			handler: HandleSnapshotNodeLatencyDistribution(mgr),
			path:    "/api/v1/metrics/snapshots/node-latency-distribution?platform_id=missing-platform",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			tc.handler.ServeHTTP(rec, req)
			assertNotFoundError(t, rec)
		})
	}
}

func TestMetricsHandlers_ExistingPlatformStillWorks(t *testing.T) {
	mgr := newTestMetricsManager(t, "existing-platform")

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/metrics/history/traffic?platform_id=existing-platform",
		nil,
	)
	rec := httptest.NewRecorder()
	HandleHistoryTraffic(mgr).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestMetricsHandlers_GlobalEndpointsRejectPlatformDimension(t *testing.T) {
	mgr := newTestMetricsManager(t, "existing-platform")

	cases := []struct {
		name    string
		handler http.Handler
		path    string
	}{
		{
			name:    "realtime throughput",
			handler: HandleRealtimeThroughput(mgr),
			path:    "/api/v1/metrics/realtime/throughput?platform_id=existing-platform",
		},
		{
			name:    "realtime connections",
			handler: HandleRealtimeConnections(mgr),
			path:    "/api/v1/metrics/realtime/connections?platform_id=existing-platform",
		},
		{
			name:    "history probes",
			handler: HandleHistoryProbes(mgr),
			path:    "/api/v1/metrics/history/probes?platform_id=existing-platform",
		},
		{
			name:    "history node-pool",
			handler: HandleHistoryNodePool(mgr),
			path:    "/api/v1/metrics/history/node-pool?platform_id=existing-platform",
		},
		{
			name:    "snapshot node-pool",
			handler: HandleSnapshotNodePool(mgr),
			path:    "/api/v1/metrics/snapshots/node-pool?platform_id=existing-platform",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			tc.handler.ServeHTTP(rec, req)
			assertInvalidArgumentError(t, rec)
		})
	}
}

func TestMetricsHandlers_RealtimeStepSecondsMatchMetricIntervals(t *testing.T) {
	mgr := newTestMetricsManager(t, "existing-platform")
	now := time.Now()
	mgr.ThroughputRing().Push(metrics.RealtimeSample{Timestamp: now, IngressBPS: 1, EgressBPS: 2})
	mgr.ConnectionsRing().Push(metrics.RealtimeSample{Timestamp: now, InboundConns: 3, OutboundConns: 4})
	mgr.LeasesRing().Push(metrics.RealtimeSample{
		Timestamp:        now,
		LeasesByPlatform: map[string]int{"existing-platform": 5},
	})

	cases := []struct {
		name     string
		handler  http.Handler
		path     string
		wantStep float64
	}{
		{
			name:     "throughput",
			handler:  HandleRealtimeThroughput(mgr),
			path:     "/api/v1/metrics/realtime/throughput",
			wantStep: 1,
		},
		{
			name:     "connections",
			handler:  HandleRealtimeConnections(mgr),
			path:     "/api/v1/metrics/realtime/connections",
			wantStep: 5,
		},
		{
			name:     "leases",
			handler:  HandleRealtimeLeases(mgr),
			path:     "/api/v1/metrics/realtime/leases?platform_id=existing-platform",
			wantStep: 7,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			tc.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if body["step_seconds"] != tc.wantStep {
				t.Fatalf("step_seconds: got %v, want %v", body["step_seconds"], tc.wantStep)
			}
		})
	}
}

func TestMetricsHandlers_HistoryAccessLatency_SeparatesOverflowBucket(t *testing.T) {
	mgr := newTestMetricsManager(t, "existing-platform")

	bucketStart := time.Now().Add(-30 * time.Minute).Unix()
	if err := mgr.Repo().WriteLatencyBucket(bucketStart, "", []int64{4, 5, 6}); err != nil {
		t.Fatalf("WriteLatencyBucket: %v", err)
	}

	from := url.QueryEscape(time.Unix(bucketStart-1, 0).UTC().Format(time.RFC3339Nano))
	to := url.QueryEscape(time.Unix(bucketStart+2, 0).UTC().Format(time.RFC3339Nano))
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/metrics/history/access-latency?from="+from+"&to="+to,
		nil,
	)
	rec := httptest.NewRecorder()
	HandleHistoryAccessLatency(mgr).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items: got %T len=%d, want len=1", body["items"], len(items))
	}
	item := items[0].(map[string]any)

	if item["sample_count"] != float64(15) {
		t.Fatalf("sample_count: got %v, want 15", item["sample_count"])
	}
	if item["overflow_count"] != float64(6) {
		t.Fatalf("overflow_count: got %v, want 6", item["overflow_count"])
	}

	buckets, ok := item["buckets"].([]any)
	if !ok {
		t.Fatalf("buckets type: got %T", item["buckets"])
	}
	if len(buckets) != 2 {
		t.Fatalf("buckets len: got %d, want 2 (regular buckets only)", len(buckets))
	}
	if buckets[0].(map[string]any)["le_ms"] != float64(100) {
		t.Fatalf("bucket[0].le_ms: got %v, want 100", buckets[0].(map[string]any)["le_ms"])
	}
	if buckets[1].(map[string]any)["le_ms"] != float64(200) {
		t.Fatalf("bucket[1].le_ms: got %v, want 200", buckets[1].(map[string]any)["le_ms"])
	}
}

func TestMetricsHandlers_SnapshotNodeLatencyDistribution_NoDuplicateOverflowBoundary(t *testing.T) {
	mgr := newTestMetricsManagerWithNodeLatency(
		t,
		testNodeLatencyProvider{
			global: []float64{100, 3000, 3001},
		},
		"existing-platform",
	)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/metrics/snapshots/node-latency-distribution",
		nil,
	)
	rec := httptest.NewRecorder()
	HandleSnapshotNodeLatencyDistribution(mgr).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	if body["sample_count"] != float64(3) {
		t.Fatalf("sample_count: got %v, want 3", body["sample_count"])
	}
	if body["overflow_count"] != float64(1) {
		t.Fatalf("overflow_count: got %v, want 1", body["overflow_count"])
	}

	buckets, ok := body["buckets"].([]any)
	if !ok {
		t.Fatalf("buckets type: got %T", body["buckets"])
	}
	le3000Count := 0
	var countAt100, countAt3000 float64
	for _, raw := range buckets {
		b := raw.(map[string]any)
		le := b["le_ms"].(float64)
		count := b["count"].(float64)
		if le == 100 {
			countAt100 = count
		}
		if le == 3000 {
			le3000Count++
			countAt3000 = count
		}
	}
	if le3000Count != 1 {
		t.Fatalf("le_ms=3000 bucket count: got %d, want 1", le3000Count)
	}
	if countAt100 != 1 {
		t.Fatalf("count at le_ms=100: got %v, want 1", countAt100)
	}
	if countAt3000 != 1 {
		t.Fatalf("count at le_ms=3000: got %v, want 1", countAt3000)
	}
}
