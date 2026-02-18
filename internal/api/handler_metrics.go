package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/resin-proxy/resin/internal/metrics"
)

// ---- shared time-range parsing ----

// parseMetricsTimeRange extracts from/to from query params (RFC3339Nano).
// Defaults: to=now, from=to-1h. Returns 400 on parse error or from>=to.
func parseMetricsTimeRange(w http.ResponseWriter, r *http.Request) (from, to time.Time, ok bool) {
	q := r.URL.Query()
	to = time.Now()
	from = to.Add(-1 * time.Hour)

	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid 'to': expected RFC3339Nano")
			return time.Time{}, time.Time{}, false
		}
		to = t
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid 'from': expected RFC3339Nano")
			return time.Time{}, time.Time{}, false
		}
		from = t
	} else {
		from = to.Add(-1 * time.Hour)
	}

	if !from.Before(to) {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "'from' must be before 'to'")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func ensureMetricsPlatformExists(mgr *metrics.Manager, w http.ResponseWriter, platformID string) bool {
	psp := mgr.PlatformStats()
	if psp == nil {
		WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "platform stats not available")
		return false
	}
	if _, ok := psp.RoutableNodeCount(platformID); !ok {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "platform not found")
		return false
	}
	return true
}

func rejectUnsupportedPlatformDimension(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := r.URL.Query()["platform_id"]; ok {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "platform_id is not supported for this endpoint")
		return true
	}
	return false
}

// ========================================================================
// Realtime endpoints (ring buffer)
// ========================================================================

// HandleRealtimeThroughput handles GET /api/v1/metrics/realtime/throughput.
func HandleRealtimeThroughput(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectUnsupportedPlatformDimension(w, r) {
			return
		}
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}
		samples := mgr.ThroughputRing().Query(from, to)
		items := make([]map[string]interface{}, 0, len(samples))
		for _, s := range samples {
			items = append(items, map[string]interface{}{
				"ts":          s.Timestamp.UTC().Format(time.RFC3339Nano),
				"ingress_bps": s.IngressBPS,
				"egress_bps":  s.EgressBPS,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"step_seconds": mgr.ThroughputIntervalSeconds(),
			"items":        items,
		})
	})
}

// HandleRealtimeConnections handles GET /api/v1/metrics/realtime/connections.
func HandleRealtimeConnections(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectUnsupportedPlatformDimension(w, r) {
			return
		}
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}
		samples := mgr.ConnectionsRing().Query(from, to)
		items := make([]map[string]interface{}, 0, len(samples))
		for _, s := range samples {
			items = append(items, map[string]interface{}{
				"ts":                   s.Timestamp.UTC().Format(time.RFC3339Nano),
				"inbound_connections":  s.InboundConns,
				"outbound_connections": s.OutboundConns,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"step_seconds": mgr.ConnectionsIntervalSeconds(),
			"items":        items,
		})
	})
}

// HandleRealtimeLeases handles GET /api/v1/metrics/realtime/leases.
func HandleRealtimeLeases(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platformID := r.URL.Query().Get("platform_id")
		if platformID == "" {
			WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "platform_id is required")
			return
		}
		if !ensureMetricsPlatformExists(mgr, w, platformID) {
			return
		}
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}
		samples := mgr.LeasesRing().Query(from, to)
		items := make([]map[string]interface{}, 0, len(samples))
		for _, s := range samples {
			count := 0
			if s.LeasesByPlatform != nil {
				count = s.LeasesByPlatform[platformID]
			}
			items = append(items, map[string]interface{}{
				"ts":            s.Timestamp.UTC().Format(time.RFC3339Nano),
				"active_leases": count,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"platform_id":  platformID,
			"step_seconds": mgr.LeasesIntervalSeconds(),
			"items":        items,
		})
	})
}

// ========================================================================
// History endpoints (metrics.db bucket)
// ========================================================================

// HandleHistoryTraffic handles GET /api/v1/metrics/history/traffic.
func HandleHistoryTraffic(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}
		platformID := r.URL.Query().Get("platform_id")
		if platformID != "" && !ensureMetricsPlatformExists(mgr, w, platformID) {
			return
		}

		rows, err := mgr.Repo().QueryTraffic(from.Unix(), to.Unix(), platformID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		items := make([]map[string]interface{}, 0, len(rows))
		for _, row := range rows {
			items = append(items, map[string]interface{}{
				"bucket_start":  time.Unix(row.BucketStartUnix, 0).UTC().Format(time.RFC3339Nano),
				"bucket_end":    time.Unix(row.BucketStartUnix+int64(mgr.BucketSeconds()), 0).UTC().Format(time.RFC3339Nano),
				"ingress_bytes": row.IngressBytes,
				"egress_bytes":  row.EgressBytes,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"bucket_seconds": mgr.BucketSeconds(),
			"items":          items,
		})
	})
}

// HandleHistoryRequests handles GET /api/v1/metrics/history/requests.
func HandleHistoryRequests(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}
		platformID := r.URL.Query().Get("platform_id")
		if platformID != "" && !ensureMetricsPlatformExists(mgr, w, platformID) {
			return
		}

		rows, err := mgr.Repo().QueryRequests(from.Unix(), to.Unix(), platformID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		items := make([]map[string]interface{}, 0, len(rows))
		for _, row := range rows {
			var rate float64
			if row.TotalRequests > 0 {
				rate = float64(row.SuccessRequests) / float64(row.TotalRequests)
			}
			items = append(items, map[string]interface{}{
				"bucket_start":     time.Unix(row.BucketStartUnix, 0).UTC().Format(time.RFC3339Nano),
				"bucket_end":       time.Unix(row.BucketStartUnix+int64(mgr.BucketSeconds()), 0).UTC().Format(time.RFC3339Nano),
				"total_requests":   row.TotalRequests,
				"success_requests": row.SuccessRequests,
				"success_rate":     rate,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"bucket_seconds": mgr.BucketSeconds(),
			"items":          items,
		})
	})
}

// HandleHistoryAccessLatency handles GET /api/v1/metrics/history/access-latency.
func HandleHistoryAccessLatency(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}
		platformID := r.URL.Query().Get("platform_id")
		if platformID != "" && !ensureMetricsPlatformExists(mgr, w, platformID) {
			return
		}

		rows, err := mgr.Repo().QueryAccessLatency(from.Unix(), to.Unix(), platformID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}

		snap := mgr.Collector().Snapshot()
		items := make([]map[string]interface{}, 0, len(rows))
		for _, row := range rows {
			// Parse buckets_json into []int64.
			var bucketCounts []int64
			if row.BucketsJSON != "" {
				_ = json.Unmarshal([]byte(row.BucketsJSON), &bucketCounts)
			}

			regularBuckets := bucketCounts
			var overflowCount int64
			// Storage keeps the overflow bucket as the last element.
			if len(bucketCounts) >= 2 {
				regularBuckets = bucketCounts[:len(bucketCounts)-1]
				overflowCount = bucketCounts[len(bucketCounts)-1]
			}

			sampleCount := overflowCount
			histBuckets := make([]map[string]interface{}, 0, len(regularBuckets))
			for i, c := range regularBuckets {
				sampleCount += c
				leMs := (i + 1) * snap.LatencyBinMs
				if leMs > snap.LatencyOverMs {
					leMs = snap.LatencyOverMs
				}
				histBuckets = append(histBuckets, map[string]interface{}{
					"le_ms": leMs,
					"count": c,
				})
			}

			items = append(items, map[string]interface{}{
				"bucket_start":   time.Unix(row.BucketStartUnix, 0).UTC().Format(time.RFC3339Nano),
				"bucket_end":     time.Unix(row.BucketStartUnix+int64(mgr.BucketSeconds()), 0).UTC().Format(time.RFC3339Nano),
				"sample_count":   sampleCount,
				"buckets":        histBuckets,
				"overflow_count": overflowCount,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"bucket_seconds": mgr.BucketSeconds(),
			"bin_width_ms":   snap.LatencyBinMs,
			"overflow_ms":    snap.LatencyOverMs,
			"items":          items,
		})
	})
}

// HandleHistoryProbes handles GET /api/v1/metrics/history/probes.
func HandleHistoryProbes(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectUnsupportedPlatformDimension(w, r) {
			return
		}
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}

		rows, err := mgr.Repo().QueryProbes(from.Unix(), to.Unix())
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		items := make([]map[string]interface{}, 0, len(rows))
		for _, row := range rows {
			items = append(items, map[string]interface{}{
				"bucket_start": time.Unix(row.BucketStartUnix, 0).UTC().Format(time.RFC3339Nano),
				"bucket_end":   time.Unix(row.BucketStartUnix+int64(mgr.BucketSeconds()), 0).UTC().Format(time.RFC3339Nano),
				"total_count":  row.TotalCount,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"bucket_seconds": mgr.BucketSeconds(),
			"items":          items,
		})
	})
}

// HandleHistoryNodePool handles GET /api/v1/metrics/history/node-pool.
func HandleHistoryNodePool(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectUnsupportedPlatformDimension(w, r) {
			return
		}
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}

		rows, err := mgr.Repo().QueryNodePool(from.Unix(), to.Unix())
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		items := make([]map[string]interface{}, 0, len(rows))
		for _, row := range rows {
			items = append(items, map[string]interface{}{
				"bucket_start":    time.Unix(row.BucketStartUnix, 0).UTC().Format(time.RFC3339Nano),
				"bucket_end":      time.Unix(row.BucketStartUnix+int64(mgr.BucketSeconds()), 0).UTC().Format(time.RFC3339Nano),
				"total_nodes":     row.TotalNodes,
				"healthy_nodes":   row.HealthyNodes,
				"egress_ip_count": row.EgressIPCount,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"bucket_seconds": mgr.BucketSeconds(),
			"items":          items,
		})
	})
}

// HandleHistoryLeaseLifetime handles GET /api/v1/metrics/history/lease-lifetime.
func HandleHistoryLeaseLifetime(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platformID := r.URL.Query().Get("platform_id")
		if platformID == "" {
			WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "platform_id is required")
			return
		}
		if !ensureMetricsPlatformExists(mgr, w, platformID) {
			return
		}
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}

		rows, err := mgr.Repo().QueryLeaseLifetime(from.Unix(), to.Unix(), platformID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		items := make([]map[string]interface{}, 0, len(rows))
		for _, row := range rows {
			items = append(items, map[string]interface{}{
				"bucket_start": time.Unix(row.BucketStartUnix, 0).UTC().Format(time.RFC3339Nano),
				"bucket_end":   time.Unix(row.BucketStartUnix+int64(mgr.BucketSeconds()), 0).UTC().Format(time.RFC3339Nano),
				"sample_count": row.SampleCount,
				"p1_ms":        row.P1Ms,
				"p5_ms":        row.P5Ms,
				"p50_ms":       row.P50Ms,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"platform_id":    platformID,
			"bucket_seconds": mgr.BucketSeconds(),
			"items":          items,
		})
	})
}

// ========================================================================
// Snapshot endpoints (realtime, no persistence)
// ========================================================================

// HandleSnapshotNodePool handles GET /api/v1/metrics/snapshots/node-pool.
func HandleSnapshotNodePool(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectUnsupportedPlatformDimension(w, r) {
			return
		}
		stats := mgr.NodePoolStats()
		if stats == nil {
			WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "node pool stats not available")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"generated_at":    time.Now().UTC().Format(time.RFC3339Nano),
			"total_nodes":     stats.TotalNodes(),
			"healthy_nodes":   stats.HealthyNodes(),
			"egress_ip_count": stats.EgressIPCount(),
		})
	})
}

// HandleSnapshotPlatformNodePool handles GET /api/v1/metrics/snapshots/platform-node-pool.
func HandleSnapshotPlatformNodePool(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platformID := r.URL.Query().Get("platform_id")
		if platformID == "" {
			WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "platform_id is required")
			return
		}
		psp := mgr.PlatformStats()
		if psp == nil {
			WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "platform stats not available")
			return
		}
		routable, ok := psp.RoutableNodeCount(platformID)
		if !ok {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "platform not found")
			return
		}
		egressCount, _ := psp.PlatformEgressIPCount(platformID)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"generated_at":        time.Now().UTC().Format(time.RFC3339Nano),
			"platform_id":         platformID,
			"routable_node_count": routable,
			"egress_ip_count":     egressCount,
		})
	})
}

// HandleSnapshotNodeLatencyDistribution handles GET /api/v1/metrics/snapshots/node-latency-distribution.
// This returns a histogram of per-node authority-domain EWMA latencies, NOT
// the per-request access latency stored in the Collector.
func HandleSnapshotNodeLatencyDistribution(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platformID := r.URL.Query().Get("platform_id")
		scope := "global"
		if platformID != "" {
			scope = "platform"
			if !ensureMetricsPlatformExists(mgr, w, platformID) {
				return
			}
		}

		nlp := mgr.NodeLatency()
		if nlp == nil {
			WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "node latency provider not available")
			return
		}

		snap := mgr.Collector().Snapshot()
		binMs := snap.LatencyBinMs
		overMs := snap.LatencyOverMs
		if binMs <= 0 {
			binMs = 50
		}
		if overMs <= 0 {
			overMs = 5000
		}
		regularBins := (overMs + binMs - 1) / binMs // ceil(over/bin)
		if regularBins <= 0 {
			regularBins = 1
		}

		ewmas := nlp.CollectNodeEWMAs(platformID)

		// Build histogram from EWMA values.
		bucketCounts := make([]int64, regularBins)
		var overflowCount int64
		for _, ms := range ewmas {
			if ms > float64(overMs) {
				overflowCount++
				continue
			}
			idx := 0
			if ms > 0 {
				idx = int((ms - 1) / float64(binMs))
			}
			if idx >= regularBins {
				idx = regularBins - 1
			}
			if idx < 0 {
				idx = 0
			}
			bucketCounts[idx]++
		}

		sampleCount := overflowCount
		histBuckets := make([]map[string]interface{}, 0, regularBins)
		for i, c := range bucketCounts {
			sampleCount += c
			leMs := (i + 1) * binMs
			if leMs > overMs {
				leMs = overMs
			}
			histBuckets = append(histBuckets, map[string]interface{}{
				"le_ms": leMs,
				"count": c,
			})
		}

		resp := map[string]interface{}{
			"generated_at":   time.Now().UTC().Format(time.RFC3339Nano),
			"scope":          scope,
			"bin_width_ms":   binMs,
			"overflow_ms":    overMs,
			"sample_count":   sampleCount,
			"buckets":        histBuckets,
			"overflow_count": overflowCount,
		}
		if platformID != "" {
			resp["platform_id"] = platformID
		}

		writeJSON(w, http.StatusOK, resp)
	})
}

// ---- helper to parse int query param ----
func queryInt(r *http.Request, param string, defaultVal int) int {
	v := r.URL.Query().Get(param)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}
