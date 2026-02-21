package metrics

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

func (m *Manager) prepareHistoryRead(now time.Time) error {
	if m.repo == nil {
		return fmt.Errorf("metrics repo is nil")
	}
	// Ensure current bucket state is advanced even if bucketLoop is delayed.
	m.advanceAndMaybeFlush(now)
	// Opportunistically persist due/pending buckets.
	m.flushPendingTasks("[metrics] history-triggered persistence failed, will retry next tick")
	return nil
}

func (m *Manager) QueryHistoryTraffic(fromUnix, toUnix int64) ([]TrafficBucketRow, error) {
	if err := m.prepareHistoryRead(time.Now()); err != nil {
		return nil, err
	}
	rows, err := m.repo.QueryTraffic(fromUnix, toUnix)
	if err != nil {
		return nil, err
	}

	currentBucketStart, currentIngress, currentEgress := m.bucket.SnapshotTraffic()
	if bucketInRangeUnix(currentBucketStart, fromUnix, toUnix) {
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
			rows = append(rows, TrafficBucketRow{
				BucketStartUnix: currentBucketStart,
				IngressBytes:    currentIngress,
				EgressBytes:     currentEgress,
			})
			sort.Slice(rows, func(i, j int) bool { return rows[i].BucketStartUnix < rows[j].BucketStartUnix })
		}
	}
	return rows, nil
}

func (m *Manager) QueryHistoryRequests(fromUnix, toUnix int64, platformID string) ([]RequestBucketRow, error) {
	if err := m.prepareHistoryRead(time.Now()); err != nil {
		return nil, err
	}
	rows, err := m.repo.QueryRequests(fromUnix, toUnix, platformID)
	if err != nil {
		return nil, err
	}

	currentBucketStart, currentTotal, currentSuccess := m.bucket.SnapshotRequests(platformID)
	if bucketInRangeUnix(currentBucketStart, fromUnix, toUnix) {
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
			rows = append(rows, RequestBucketRow{
				BucketStartUnix: currentBucketStart,
				PlatformID:      platformID,
				TotalRequests:   currentTotal,
				SuccessRequests: currentSuccess,
			})
			sort.Slice(rows, func(i, j int) bool { return rows[i].BucketStartUnix < rows[j].BucketStartUnix })
		}
	}
	return rows, nil
}

func (m *Manager) QueryHistoryAccessLatency(fromUnix, toUnix int64, platformID string) ([]AccessLatencyBucketRow, error) {
	if err := m.prepareHistoryRead(time.Now()); err != nil {
		return nil, err
	}
	rows, err := m.repo.QueryAccessLatency(fromUnix, toUnix, platformID)
	if err != nil {
		return nil, err
	}

	currentBucketStart := m.bucket.CurrentBucketStartUnix()
	currentBuckets := m.currentAccessLatencyBuckets(platformID)
	if bucketInRangeUnix(currentBucketStart, fromUnix, toUnix) {
		merged := false
		for i := range rows {
			if rows[i].BucketStartUnix != currentBucketStart {
				continue
			}
			persisted := decodeLatencyBucketsJSON(rows[i].BucketsJSON)
			rows[i].BucketsJSON = encodeLatencyBucketsJSON(mergeLatencyBuckets(persisted, currentBuckets))
			merged = true
			break
		}
		if !merged {
			rows = append(rows, AccessLatencyBucketRow{
				BucketStartUnix: currentBucketStart,
				PlatformID:      platformID,
				BucketsJSON:     encodeLatencyBucketsJSON(currentBuckets),
			})
			sort.Slice(rows, func(i, j int) bool { return rows[i].BucketStartUnix < rows[j].BucketStartUnix })
		}
	}
	return rows, nil
}

func (m *Manager) QueryHistoryProbes(fromUnix, toUnix int64) ([]ProbeBucketRow, error) {
	if err := m.prepareHistoryRead(time.Now()); err != nil {
		return nil, err
	}
	rows, err := m.repo.QueryProbes(fromUnix, toUnix)
	if err != nil {
		return nil, err
	}

	currentBucketStart, currentTotal := m.bucket.SnapshotProbes()
	if bucketInRangeUnix(currentBucketStart, fromUnix, toUnix) {
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
			rows = append(rows, ProbeBucketRow{
				BucketStartUnix: currentBucketStart,
				TotalCount:      currentTotal,
			})
			sort.Slice(rows, func(i, j int) bool { return rows[i].BucketStartUnix < rows[j].BucketStartUnix })
		}
	}
	return rows, nil
}

func (m *Manager) QueryHistoryNodePool(fromUnix, toUnix int64) ([]NodePoolBucketRow, error) {
	if err := m.prepareHistoryRead(time.Now()); err != nil {
		return nil, err
	}
	rows, err := m.repo.QueryNodePool(fromUnix, toUnix)
	if err != nil {
		return nil, err
	}

	currentBucketStart := m.bucket.CurrentBucketStartUnix()
	if m.runtimeStats != nil && bucketInRangeUnix(currentBucketStart, fromUnix, toUnix) {
		totalNodes := m.runtimeStats.TotalNodes()
		healthyNodes := m.runtimeStats.HealthyNodes()
		egressIPCount := m.runtimeStats.EgressIPCount()
		merged := false
		for i := range rows {
			if rows[i].BucketStartUnix != currentBucketStart {
				continue
			}
			// Node-pool is a point-in-time snapshot; in-memory values override.
			rows[i].TotalNodes = totalNodes
			rows[i].HealthyNodes = healthyNodes
			rows[i].EgressIPCount = egressIPCount
			merged = true
			break
		}
		if !merged {
			rows = append(rows, NodePoolBucketRow{
				BucketStartUnix: currentBucketStart,
				TotalNodes:      totalNodes,
				HealthyNodes:    healthyNodes,
				EgressIPCount:   egressIPCount,
			})
			sort.Slice(rows, func(i, j int) bool { return rows[i].BucketStartUnix < rows[j].BucketStartUnix })
		}
	}
	return rows, nil
}

func (m *Manager) QueryHistoryLeaseLifetime(fromUnix, toUnix int64, platformID string) ([]LeaseLifetimeBucketRow, error) {
	if err := m.prepareHistoryRead(time.Now()); err != nil {
		return nil, err
	}
	rows, err := m.repo.QueryLeaseLifetime(fromUnix, toUnix, platformID)
	if err != nil {
		return nil, err
	}

	currentBucketStart, samples := m.bucket.SnapshotLeaseLifetimeSamples(platformID)
	currentSampleCount := len(samples)
	currentP1, currentP5, currentP50 := computePercentiles(samples)
	if bucketInRangeUnix(currentBucketStart, fromUnix, toUnix) {
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
			rows = append(rows, LeaseLifetimeBucketRow{
				BucketStartUnix: currentBucketStart,
				PlatformID:      platformID,
				SampleCount:     currentSampleCount,
				P1Ms:            currentP1,
				P5Ms:            currentP5,
				P50Ms:           currentP50,
			})
			sort.Slice(rows, func(i, j int) bool { return rows[i].BucketStartUnix < rows[j].BucketStartUnix })
		}
	}
	return rows, nil
}

func (m *Manager) currentAccessLatencyBuckets(platformID string) []int64 {
	if platformID == "" {
		snap := m.collector.Snapshot()
		return append([]int64(nil), snap.LatencyBuckets...)
	}
	snap, ok := m.collector.PlatformSnapshot(platformID)
	if !ok {
		globalSnap := m.collector.Snapshot()
		return make([]int64, len(globalSnap.LatencyBuckets))
	}
	return append([]int64(nil), snap.LatencyBuckets...)
}

func bucketInRangeUnix(bucketStartUnix, fromUnix, toUnix int64) bool {
	return bucketStartUnix >= fromUnix && bucketStartUnix <= toUnix
}

func decodeLatencyBucketsJSON(raw string) []int64 {
	if raw == "" {
		return nil
	}
	var buckets []int64
	_ = json.Unmarshal([]byte(raw), &buckets)
	return buckets
}

func encodeLatencyBucketsJSON(buckets []int64) string {
	payload, err := json.Marshal(buckets)
	if err != nil {
		return "[]"
	}
	return string(payload)
}

func mergeLatencyBuckets(base, delta []int64) []int64 {
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
