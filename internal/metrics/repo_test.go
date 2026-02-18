package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMetricsRepo_WriteAndQuery(t *testing.T) {
	repo, err := NewMetricsRepo(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("NewMetricsRepo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	bucketStart := time.Now().Add(-time.Minute).Unix()
	err = repo.WriteBucket(&BucketFlushData{
		BucketStartUnix: bucketStart,
		Traffic: map[string]trafficAccum{
			"":       {IngressBytes: 100, EgressBytes: 200},
			"plat-1": {IngressBytes: 300, EgressBytes: 400},
		},
		Requests: map[string]requestAccum{
			"":       {Total: 5, Success: 4},
			"plat-1": {Total: 3, Success: 2},
		},
		Probes: probeAccum{Total: 7},
		LeaseLifetimes: map[string]*leaseLifeAccum{
			"plat-1": {Samples: []int64{int64(time.Second), int64(2 * time.Second), int64(3 * time.Second)}},
		},
	})
	if err != nil {
		t.Fatalf("WriteBucket: %v", err)
	}
	if err := repo.WriteNodePoolSnapshot(bucketStart, 20, 12, 6); err != nil {
		t.Fatalf("WriteNodePoolSnapshot: %v", err)
	}
	if err := repo.WriteLatencyBucket(bucketStart, "", []int64{1, 2, 3}); err != nil {
		t.Fatalf("WriteLatencyBucket global: %v", err)
	}
	if err := repo.WriteLatencyBucket(bucketStart, "plat-1", []int64{4, 5, 6}); err != nil {
		t.Fatalf("WriteLatencyBucket platform: %v", err)
	}

	from, to := bucketStart-10, bucketStart+10
	traffic, err := repo.QueryTraffic(from, to, "plat-1")
	if err != nil {
		t.Fatalf("QueryTraffic: %v", err)
	}
	if len(traffic) != 1 || traffic[0].IngressBytes != 300 || traffic[0].EgressBytes != 400 {
		t.Fatalf("unexpected traffic rows: %+v", traffic)
	}

	requests, err := repo.QueryRequests(from, to, "plat-1")
	if err != nil {
		t.Fatalf("QueryRequests: %v", err)
	}
	if len(requests) != 1 || requests[0].TotalRequests != 3 || requests[0].SuccessRequests != 2 {
		t.Fatalf("unexpected request rows: %+v", requests)
	}

	probes, err := repo.QueryProbes(from, to)
	if err != nil {
		t.Fatalf("QueryProbes: %v", err)
	}
	if len(probes) != 1 || probes[0].TotalCount != 7 {
		t.Fatalf("unexpected probe rows: %+v", probes)
	}

	nodePool, err := repo.QueryNodePool(from, to)
	if err != nil {
		t.Fatalf("QueryNodePool: %v", err)
	}
	if len(nodePool) != 1 || nodePool[0].TotalNodes != 20 || nodePool[0].HealthyNodes != 12 || nodePool[0].EgressIPCount != 6 {
		t.Fatalf("unexpected node-pool rows: %+v", nodePool)
	}

	latency, err := repo.QueryAccessLatency(from, to, "plat-1")
	if err != nil {
		t.Fatalf("QueryAccessLatency: %v", err)
	}
	if len(latency) != 1 || latency[0].BucketsJSON == "" {
		t.Fatalf("unexpected latency rows: %+v", latency)
	}

	leaseLife, err := repo.QueryLeaseLifetime(from, to, "plat-1")
	if err != nil {
		t.Fatalf("QueryLeaseLifetime: %v", err)
	}
	if len(leaseLife) != 1 {
		t.Fatalf("unexpected lease lifetime rows: %+v", leaseLife)
	}
	if leaseLife[0].SampleCount != 3 || leaseLife[0].P1Ms != 1000 || leaseLife[0].P5Ms != 1000 || leaseLife[0].P50Ms != 2000 {
		t.Fatalf("unexpected lease lifetime values: %+v", leaseLife[0])
	}
}

func TestMetricsRepo_NewMetricsRepoCreatesParentDir(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "nested", "metrics.db")

	repo, err := NewMetricsRepo(dbPath)
	if err != nil {
		t.Fatalf("NewMetricsRepo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	if _, err := os.Stat(filepath.Dir(dbPath)); err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
}

func TestMetricsRepo_QueryGlobalOnlyWhenPlatformEmpty(t *testing.T) {
	repo, err := NewMetricsRepo(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("NewMetricsRepo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	bucketStart := time.Now().Add(-time.Minute).Unix()
	err = repo.WriteBucket(&BucketFlushData{
		BucketStartUnix: bucketStart,
		Traffic: map[string]trafficAccum{
			"":       {IngressBytes: 10, EgressBytes: 20},
			"plat-1": {IngressBytes: 30, EgressBytes: 40},
		},
		Requests: map[string]requestAccum{
			"":       {Total: 1, Success: 1},
			"plat-1": {Total: 2, Success: 1},
		},
	})
	if err != nil {
		t.Fatalf("WriteBucket: %v", err)
	}
	if err := repo.WriteLatencyBucket(bucketStart, "", []int64{1, 2}); err != nil {
		t.Fatalf("WriteLatencyBucket global: %v", err)
	}
	if err := repo.WriteLatencyBucket(bucketStart, "plat-1", []int64{3, 4}); err != nil {
		t.Fatalf("WriteLatencyBucket platform: %v", err)
	}

	from, to := bucketStart-1, bucketStart+1

	trafficRows, err := repo.QueryTraffic(from, to, "")
	if err != nil {
		t.Fatalf("QueryTraffic global: %v", err)
	}
	if len(trafficRows) != 1 {
		t.Fatalf("QueryTraffic global row count: got %d, want 1", len(trafficRows))
	}
	if trafficRows[0].PlatformID != "" {
		t.Fatalf("QueryTraffic global platform_id: got %q, want empty", trafficRows[0].PlatformID)
	}

	requestRows, err := repo.QueryRequests(from, to, "")
	if err != nil {
		t.Fatalf("QueryRequests global: %v", err)
	}
	if len(requestRows) != 1 {
		t.Fatalf("QueryRequests global row count: got %d, want 1", len(requestRows))
	}
	if requestRows[0].PlatformID != "" {
		t.Fatalf("QueryRequests global platform_id: got %q, want empty", requestRows[0].PlatformID)
	}

	latRows, err := repo.QueryAccessLatency(from, to, "")
	if err != nil {
		t.Fatalf("QueryAccessLatency global: %v", err)
	}
	if len(latRows) != 1 {
		t.Fatalf("QueryAccessLatency global row count: got %d, want 1", len(latRows))
	}
	if latRows[0].PlatformID != "" {
		t.Fatalf("QueryAccessLatency global platform_id: got %q, want empty", latRows[0].PlatformID)
	}

	assertGlobalDimensionStoredAsNULL := func(table string) {
		t.Helper()
		var nullCount int
		if err := repo.db.QueryRow(
			"SELECT COUNT(*) FROM "+table+" WHERE bucket_start_unix = ? AND platform_id IS NULL",
			bucketStart,
		).Scan(&nullCount); err != nil {
			t.Fatalf("count NULL platform_id in %s: %v", table, err)
		}
		if nullCount != 1 {
			t.Fatalf("%s global rows with NULL platform_id: got %d, want 1", table, nullCount)
		}

		var emptyCount int
		if err := repo.db.QueryRow(
			"SELECT COUNT(*) FROM "+table+" WHERE bucket_start_unix = ? AND platform_id = ''",
			bucketStart,
		).Scan(&emptyCount); err != nil {
			t.Fatalf("count empty-string platform_id in %s: %v", table, err)
		}
		if emptyCount != 0 {
			t.Fatalf("%s global rows with empty-string platform_id: got %d, want 0", table, emptyCount)
		}
	}

	assertGlobalDimensionStoredAsNULL("metric_traffic_bucket")
	assertGlobalDimensionStoredAsNULL("metric_request_bucket")
	assertGlobalDimensionStoredAsNULL("metric_access_latency_bucket")
}
