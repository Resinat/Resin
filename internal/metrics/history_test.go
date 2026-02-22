package metrics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/proxy"
)

func TestQueryHistoryTraffic_AdvancesStaleBucketWithoutBucketLoop(t *testing.T) {
	repo, err := NewMetricsRepo(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("NewMetricsRepo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	const bucketSec = 3600
	mgr := NewManager(ManagerConfig{
		Repo:                        repo,
		BucketSeconds:               bucketSec,
		LatencyBinMs:                100,
		LatencyOverflowMs:           3000,
		ThroughputRealtimeCapacity:  8,
		ThroughputIntervalSec:       1,
		ConnectionsRealtimeCapacity: 8,
		ConnectionsIntervalSec:      5,
		LeasesRealtimeCapacity:      8,
		LeasesIntervalSec:           5,
	})

	nowUnix := time.Now().Unix()
	currentAligned := (nowUnix / bucketSec) * bucketSec
	staleStart := currentAligned - bucketSec

	mgr.bucket.mu.Lock()
	mgr.bucket.currentStart = staleStart
	mgr.bucket.mu.Unlock()

	mgr.OnTrafficDelta(111, 222)

	from := currentAligned - 300
	to := currentAligned + 300
	rows, err := mgr.QueryHistoryTraffic(from, to)
	if err != nil {
		t.Fatalf("QueryHistoryTraffic: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len: got %d, want 1", len(rows))
	}
	if rows[0].BucketStartUnix != currentAligned {
		t.Fatalf("current bucket_start_unix: got %d, want %d", rows[0].BucketStartUnix, currentAligned)
	}
	if rows[0].IngressBytes != 0 || rows[0].EgressBytes != 0 {
		t.Fatalf("current bucket traffic: got ingress=%d egress=%d, want 0/0", rows[0].IngressBytes, rows[0].EgressBytes)
	}

	flushed, err := repo.QueryTraffic(staleStart, staleStart)
	if err != nil {
		t.Fatalf("repo.QueryTraffic(stale): %v", err)
	}
	if len(flushed) != 1 {
		t.Fatalf("stale rows len: got %d, want 1", len(flushed))
	}
	if flushed[0].IngressBytes != 111 || flushed[0].EgressBytes != 222 {
		t.Fatalf("stale row traffic: got ingress=%d egress=%d, want 111/222", flushed[0].IngressBytes, flushed[0].EgressBytes)
	}
}

func TestQueryHistoryRequests_AdvancesAndFlushesAggregatedCounters(t *testing.T) {
	repo, err := NewMetricsRepo(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("NewMetricsRepo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	const bucketSec = 3600
	const platformID = "plat-1"

	mgr := NewManager(ManagerConfig{
		Repo:                        repo,
		BucketSeconds:               bucketSec,
		LatencyBinMs:                100,
		LatencyOverflowMs:           3000,
		ThroughputRealtimeCapacity:  8,
		ThroughputIntervalSec:       1,
		ConnectionsRealtimeCapacity: 8,
		ConnectionsIntervalSec:      5,
		LeasesRealtimeCapacity:      8,
		LeasesIntervalSec:           5,
	})

	nowUnix := time.Now().Unix()
	currentAligned := (nowUnix / bucketSec) * bucketSec
	staleStart := currentAligned - bucketSec

	mgr.bucket.mu.Lock()
	mgr.bucket.currentStart = staleStart
	mgr.bucket.mu.Unlock()

	mgr.OnRequestFinished(proxy.RequestFinishedEvent{
		PlatformID: platformID,
		NetOK:      true,
		DurationNs: int64(120 * time.Millisecond),
	})
	mgr.OnRequestFinished(proxy.RequestFinishedEvent{
		PlatformID: platformID,
		NetOK:      false,
		DurationNs: int64(240 * time.Millisecond),
	})

	from := currentAligned - 300
	to := currentAligned + 300
	rows, err := mgr.QueryHistoryRequests(from, to, platformID)
	if err != nil {
		t.Fatalf("QueryHistoryRequests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len: got %d, want 1", len(rows))
	}
	if rows[0].BucketStartUnix != currentAligned {
		t.Fatalf("current bucket_start_unix: got %d, want %d", rows[0].BucketStartUnix, currentAligned)
	}
	if rows[0].TotalRequests != 0 || rows[0].SuccessRequests != 0 {
		t.Fatalf("current bucket requests: got total=%d success=%d, want 0/0", rows[0].TotalRequests, rows[0].SuccessRequests)
	}

	flushed, err := repo.QueryRequests(staleStart, staleStart, platformID)
	if err != nil {
		t.Fatalf("repo.QueryRequests(stale): %v", err)
	}
	if len(flushed) != 1 {
		t.Fatalf("stale rows len: got %d, want 1", len(flushed))
	}
	if flushed[0].TotalRequests != 2 || flushed[0].SuccessRequests != 1 {
		t.Fatalf("stale row requests: got total=%d success=%d, want 2/1", flushed[0].TotalRequests, flushed[0].SuccessRequests)
	}
}
