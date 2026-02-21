package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/resin-proxy/resin/internal/metrics"
)

// ---- shared time-range parsing ----

// parseMetricsTimeRange extracts from/to from query params (RFC3339Nano).
// Defaults: to=now, from=to-1h. Returns 400 on parse error or from>=to.
func parseMetricsTimeRange(w http.ResponseWriter, r *http.Request) (from, to time.Time, ok bool) {
	q := r.URL.Query()
	to = time.Now()

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
	stats := mgr.RuntimeStats()
	if stats == nil {
		WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "platform stats not available")
		return false
	}
	if _, ok := stats.RoutableNodeCount(platformID); !ok {
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

func formatTimestamp(ts time.Time) string {
	return ts.UTC().Format(time.RFC3339Nano)
}

func bucketWindow(bucketStartUnix int64, bucketSeconds int) (string, string) {
	start := formatTimestamp(time.Unix(bucketStartUnix, 0))
	end := formatTimestamp(time.Unix(bucketStartUnix+int64(bucketSeconds), 0))
	return start, end
}

func mapItems[T any](src []T, mapFn func(T) map[string]any) []map[string]any {
	items := make([]map[string]any, 0, len(src))
	for _, item := range src {
		items = append(items, mapFn(item))
	}
	return items
}

func sumLeasesByPlatform(leasesByPlatform map[string]int) int {
	total := 0
	for _, count := range leasesByPlatform {
		total += count
	}
	return total
}

func requiredPlatformID(mgr *metrics.Manager, w http.ResponseWriter, r *http.Request) (string, bool) {
	platformID := r.URL.Query().Get("platform_id")
	if platformID == "" {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "platform_id is required")
		return "", false
	}
	if !ensureMetricsPlatformExists(mgr, w, platformID) {
		return "", false
	}
	return platformID, true
}

func splitOverflowBucket(bucketCounts []int64) ([]int64, int64) {
	if len(bucketCounts) < 2 {
		return bucketCounts, 0
	}
	return bucketCounts[:len(bucketCounts)-1], bucketCounts[len(bucketCounts)-1]
}

func buildLatencyHistogram(regularBuckets []int64, overflowCount int64, binMs, overMs int) ([]map[string]any, int64) {
	sampleCount := overflowCount
	histBuckets := make([]map[string]any, 0, len(regularBuckets))
	for i, c := range regularBuckets {
		sampleCount += c
		// Emit upper-inclusive bucket boundary so UI can show 0-99,100-199...
		upperExclusive := (i + 1) * binMs
		if upperExclusive > overMs {
			upperExclusive = overMs
		}
		leMs := upperExclusive - 1
		if leMs < 0 {
			leMs = 0
		}
		histBuckets = append(histBuckets, map[string]any{
			"le_ms": leMs,
			"count": c,
		})
	}
	return histBuckets, sampleCount
}

func bucketInRange(bucketStartUnix, fromUnix, toUnix int64) bool {
	return bucketStartUnix >= fromUnix && bucketStartUnix <= toUnix
}

func decodeLatencyBuckets(raw string) []int64 {
	if raw == "" {
		return nil
	}
	var buckets []int64
	_ = json.Unmarshal([]byte(raw), &buckets)
	return buckets
}

func encodeLatencyBuckets(buckets []int64) string {
	payload, err := json.Marshal(buckets)
	if err != nil {
		return "[]"
	}
	return string(payload)
}

func mergeLatencyBucketCounts(base, delta []int64) []int64 {
	size := len(base)
	if len(delta) > size {
		size = len(delta)
	}
	out := make([]int64, size)
	copy(out, base)
	for i := range delta {
		out[i] += delta[i]
	}
	return out
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
		items := mapItems(samples, func(s metrics.RealtimeSample) map[string]any {
			return map[string]any{
				"ts":          formatTimestamp(s.Timestamp),
				"ingress_bps": s.IngressBPS,
				"egress_bps":  s.EgressBPS,
			}
		})
		WriteJSON(w, http.StatusOK, map[string]any{
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
		items := mapItems(samples, func(s metrics.RealtimeSample) map[string]any {
			return map[string]any{
				"ts":                   formatTimestamp(s.Timestamp),
				"inbound_connections":  s.InboundConns,
				"outbound_connections": s.OutboundConns,
			}
		})
		WriteJSON(w, http.StatusOK, map[string]any{
			"step_seconds": mgr.ConnectionsIntervalSeconds(),
			"items":        items,
		})
	})
}

// HandleRealtimeLeases handles GET /api/v1/metrics/realtime/leases.
func HandleRealtimeLeases(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platformID := r.URL.Query().Get("platform_id")
		if platformID != "" && !ensureMetricsPlatformExists(mgr, w, platformID) {
			return
		}
		scopeGlobal := platformID == ""
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}
		samples := mgr.LeasesRing().Query(from, to)
		items := mapItems(samples, func(s metrics.RealtimeSample) map[string]any {
			count := 0
			if s.LeasesByPlatform != nil {
				if scopeGlobal {
					count = sumLeasesByPlatform(s.LeasesByPlatform)
				} else {
					count = s.LeasesByPlatform[platformID]
				}
			}
			return map[string]any{
				"ts":            formatTimestamp(s.Timestamp),
				"active_leases": count,
			}
		})
		WriteJSON(w, http.StatusOK, map[string]any{
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
		if rejectUnsupportedPlatformDimension(w, r) {
			return
		}
		from, to, ok := parseMetricsTimeRange(w, r)
		if !ok {
			return
		}

		rows, err := mgr.Repo().QueryTraffic(from.Unix(), to.Unix())
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		currentBucketStart, currentIngress, currentEgress := mgr.SnapshotCurrentTrafficBucket()
		if currentBucketStart >= from.Unix() && currentBucketStart <= to.Unix() {
			merged := false
			for i := range rows {
				if rows[i].BucketStartUnix != currentBucketStart {
					continue
				}
				rows[i].IngressBytes += currentIngress
				rows[i].EgressBytes += currentEgress
				merged = true
				break
			}
			if !merged {
				rows = append(rows, metrics.TrafficBucketRow{
					BucketStartUnix: currentBucketStart,
					IngressBytes:    currentIngress,
					EgressBytes:     currentEgress,
				})
				sort.Slice(rows, func(i, j int) bool {
					return rows[i].BucketStartUnix < rows[j].BucketStartUnix
				})
			}
		}
		bucketSeconds := mgr.BucketSeconds()
		items := mapItems(rows, func(row metrics.TrafficBucketRow) map[string]any {
			bucketStart, bucketEnd := bucketWindow(row.BucketStartUnix, bucketSeconds)
			return map[string]any{
				"bucket_start":  bucketStart,
				"bucket_end":    bucketEnd,
				"ingress_bytes": row.IngressBytes,
				"egress_bytes":  row.EgressBytes,
			}
		})
		WriteJSON(w, http.StatusOK, map[string]any{
			"bucket_seconds": bucketSeconds,
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
		currentBucketStart, currentTotal, currentSuccess := mgr.SnapshotCurrentRequestsBucket(platformID)
		if bucketInRange(currentBucketStart, from.Unix(), to.Unix()) {
			merged := false
			for i := range rows {
				if rows[i].BucketStartUnix != currentBucketStart {
					continue
				}
				rows[i].TotalRequests += currentTotal
				rows[i].SuccessRequests += currentSuccess
				if rows[i].SuccessRequests > rows[i].TotalRequests {
					rows[i].SuccessRequests = rows[i].TotalRequests
				}
				merged = true
				break
			}
			if !merged {
				rows = append(rows, metrics.RequestBucketRow{
					BucketStartUnix: currentBucketStart,
					PlatformID:      platformID,
					TotalRequests:   currentTotal,
					SuccessRequests: currentSuccess,
				})
				sort.Slice(rows, func(i, j int) bool {
					return rows[i].BucketStartUnix < rows[j].BucketStartUnix
				})
			}
		}
		bucketSeconds := mgr.BucketSeconds()
		items := mapItems(rows, func(row metrics.RequestBucketRow) map[string]any {
			var rate float64
			if row.TotalRequests > 0 {
				rate = float64(row.SuccessRequests) / float64(row.TotalRequests)
			}
			bucketStart, bucketEnd := bucketWindow(row.BucketStartUnix, bucketSeconds)
			return map[string]any{
				"bucket_start":     bucketStart,
				"bucket_end":       bucketEnd,
				"total_requests":   row.TotalRequests,
				"success_requests": row.SuccessRequests,
				"success_rate":     rate,
			}
		})
		WriteJSON(w, http.StatusOK, map[string]any{
			"bucket_seconds": bucketSeconds,
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
		currentBucketStart, currentBuckets := mgr.SnapshotCurrentAccessLatencyBucket(platformID)
		if bucketInRange(currentBucketStart, from.Unix(), to.Unix()) {
			merged := false
			for i := range rows {
				if rows[i].BucketStartUnix != currentBucketStart {
					continue
				}
				persisted := decodeLatencyBuckets(rows[i].BucketsJSON)
				rows[i].BucketsJSON = encodeLatencyBuckets(mergeLatencyBucketCounts(persisted, currentBuckets))
				merged = true
				break
			}
			if !merged {
				rows = append(rows, metrics.AccessLatencyBucketRow{
					BucketStartUnix: currentBucketStart,
					PlatformID:      platformID,
					BucketsJSON:     encodeLatencyBuckets(currentBuckets),
				})
				sort.Slice(rows, func(i, j int) bool {
					return rows[i].BucketStartUnix < rows[j].BucketStartUnix
				})
			}
		}

		snap := mgr.Collector().Snapshot()
		bucketSeconds := mgr.BucketSeconds()
		items := mapItems(rows, func(row metrics.AccessLatencyBucketRow) map[string]any {
			bucketCounts := decodeLatencyBuckets(row.BucketsJSON)

			regularBuckets, overflowCount := splitOverflowBucket(bucketCounts)
			histBuckets, sampleCount := buildLatencyHistogram(regularBuckets, overflowCount, snap.LatencyBinMs, snap.LatencyOverMs)
			bucketStart, bucketEnd := bucketWindow(row.BucketStartUnix, bucketSeconds)
			return map[string]any{
				"bucket_start":   bucketStart,
				"bucket_end":     bucketEnd,
				"sample_count":   sampleCount,
				"buckets":        histBuckets,
				"overflow_count": overflowCount,
			}
		})
		WriteJSON(w, http.StatusOK, map[string]any{
			"bucket_seconds": bucketSeconds,
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
		currentBucketStart, currentTotal := mgr.SnapshotCurrentProbeBucket()
		if bucketInRange(currentBucketStart, from.Unix(), to.Unix()) {
			merged := false
			for i := range rows {
				if rows[i].BucketStartUnix != currentBucketStart {
					continue
				}
				rows[i].TotalCount += currentTotal
				merged = true
				break
			}
			if !merged {
				rows = append(rows, metrics.ProbeBucketRow{
					BucketStartUnix: currentBucketStart,
					TotalCount:      currentTotal,
				})
				sort.Slice(rows, func(i, j int) bool {
					return rows[i].BucketStartUnix < rows[j].BucketStartUnix
				})
			}
		}
		bucketSeconds := mgr.BucketSeconds()
		items := mapItems(rows, func(row metrics.ProbeBucketRow) map[string]any {
			bucketStart, bucketEnd := bucketWindow(row.BucketStartUnix, bucketSeconds)
			return map[string]any{
				"bucket_start": bucketStart,
				"bucket_end":   bucketEnd,
				"total_count":  row.TotalCount,
			}
		})
		WriteJSON(w, http.StatusOK, map[string]any{
			"bucket_seconds": bucketSeconds,
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
		currentBucketStart, totalNodes, healthyNodes, egressIPCount, ok := mgr.SnapshotCurrentNodePoolBucket()
		if ok && bucketInRange(currentBucketStart, from.Unix(), to.Unix()) {
			merged := false
			for i := range rows {
				if rows[i].BucketStartUnix != currentBucketStart {
					continue
				}
				// Node-pool is a point-in-time snapshot, so latest in-memory values replace persisted row.
				rows[i].TotalNodes = totalNodes
				rows[i].HealthyNodes = healthyNodes
				rows[i].EgressIPCount = egressIPCount
				merged = true
				break
			}
			if !merged {
				rows = append(rows, metrics.NodePoolBucketRow{
					BucketStartUnix: currentBucketStart,
					TotalNodes:      totalNodes,
					HealthyNodes:    healthyNodes,
					EgressIPCount:   egressIPCount,
				})
				sort.Slice(rows, func(i, j int) bool {
					return rows[i].BucketStartUnix < rows[j].BucketStartUnix
				})
			}
		}
		bucketSeconds := mgr.BucketSeconds()
		items := mapItems(rows, func(row metrics.NodePoolBucketRow) map[string]any {
			bucketStart, bucketEnd := bucketWindow(row.BucketStartUnix, bucketSeconds)
			return map[string]any{
				"bucket_start":    bucketStart,
				"bucket_end":      bucketEnd,
				"total_nodes":     row.TotalNodes,
				"healthy_nodes":   row.HealthyNodes,
				"egress_ip_count": row.EgressIPCount,
			}
		})
		WriteJSON(w, http.StatusOK, map[string]any{
			"bucket_seconds": bucketSeconds,
			"items":          items,
		})
	})
}

// HandleHistoryLeaseLifetime handles GET /api/v1/metrics/history/lease-lifetime.
func HandleHistoryLeaseLifetime(mgr *metrics.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platformID, ok := requiredPlatformID(mgr, w, r)
		if !ok {
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
		currentBucketStart, currentSampleCount, currentP1, currentP5, currentP50 := mgr.SnapshotCurrentLeaseLifetimeBucket(platformID)
		if bucketInRange(currentBucketStart, from.Unix(), to.Unix()) {
			merged := false
			for i := range rows {
				if rows[i].BucketStartUnix != currentBucketStart {
					continue
				}
				if rows[i].SampleCount == 0 && currentSampleCount > 0 {
					rows[i].SampleCount = currentSampleCount
					rows[i].P1Ms = currentP1
					rows[i].P5Ms = currentP5
					rows[i].P50Ms = currentP50
				}
				merged = true
				break
			}
			if !merged {
				rows = append(rows, metrics.LeaseLifetimeBucketRow{
					BucketStartUnix: currentBucketStart,
					PlatformID:      platformID,
					SampleCount:     currentSampleCount,
					P1Ms:            currentP1,
					P5Ms:            currentP5,
					P50Ms:           currentP50,
				})
				sort.Slice(rows, func(i, j int) bool {
					return rows[i].BucketStartUnix < rows[j].BucketStartUnix
				})
			}
		}
		bucketSeconds := mgr.BucketSeconds()
		items := mapItems(rows, func(row metrics.LeaseLifetimeBucketRow) map[string]any {
			bucketStart, bucketEnd := bucketWindow(row.BucketStartUnix, bucketSeconds)
			return map[string]any{
				"bucket_start": bucketStart,
				"bucket_end":   bucketEnd,
				"sample_count": row.SampleCount,
				"p1_ms":        row.P1Ms,
				"p5_ms":        row.P5Ms,
				"p50_ms":       row.P50Ms,
			}
		})
		WriteJSON(w, http.StatusOK, map[string]any{
			"platform_id":    platformID,
			"bucket_seconds": bucketSeconds,
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
		stats := mgr.RuntimeStats()
		if stats == nil {
			WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "node pool stats not available")
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"generated_at":    formatTimestamp(time.Now()),
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
		stats := mgr.RuntimeStats()
		if stats == nil {
			WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "platform stats not available")
			return
		}
		routable, ok := stats.RoutableNodeCount(platformID)
		if !ok {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "platform not found")
			return
		}
		egressCount, _ := stats.PlatformEgressIPCount(platformID)
		WriteJSON(w, http.StatusOK, map[string]any{
			"generated_at":        formatTimestamp(time.Now()),
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

		stats := mgr.RuntimeStats()
		if stats == nil {
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
		regularBins := (overMs + binMs - 1) / binMs // ceil(over/bin), buckets cover [0, overMs)
		if regularBins <= 0 {
			regularBins = 1
		}

		ewmas := stats.CollectNodeEWMAs(platformID)

		// Build histogram from EWMA values.
		bucketCounts := make([]int64, regularBins)
		var overflowCount int64
		for _, ms := range ewmas {
			if ms >= float64(overMs) {
				overflowCount++
				continue
			}
			idx := 0
			if ms >= 0 {
				idx = int(ms / float64(binMs))
			}
			if idx >= regularBins {
				idx = regularBins - 1
			}
			if idx < 0 {
				idx = 0
			}
			bucketCounts[idx]++
		}

		histBuckets, sampleCount := buildLatencyHistogram(bucketCounts, overflowCount, binMs, overMs)

		resp := map[string]any{
			"generated_at":   formatTimestamp(time.Now()),
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

		WriteJSON(w, http.StatusOK, resp)
	})
}
