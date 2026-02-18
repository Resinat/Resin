package metrics

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/proxy"
)

type managerTestNodePoolStats struct{}

func (managerTestNodePoolStats) TotalNodes() int    { return 9 }
func (managerTestNodePoolStats) HealthyNodes() int  { return 7 }
func (managerTestNodePoolStats) EgressIPCount() int { return 3 }

type managerTestLeaseCountProvider struct {
	counts map[string]int
}

func (p managerTestLeaseCountProvider) LeaseCountsByPlatform() map[string]int {
	out := make(map[string]int, len(p.counts))
	for k, v := range p.counts {
		out[k] = v
	}
	return out
}

func TestTakeSample_NormalizesThroughputToBPS(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		ThroughputRealtimeCapacity: 8,
		ThroughputIntervalSec:      5,
	})

	mgr.OnTrafficDelta("", 100, 250)
	mgr.takeThroughputSample(time.Unix(5, 0))

	sample, ok := mgr.ThroughputRing().Latest()
	if !ok {
		t.Fatal("expected sample in realtime ring")
	}
	if sample.IngressBPS != 20 {
		t.Fatalf("first sample ingress_bps: got %d, want %d", sample.IngressBPS, 20)
	}
	if sample.EgressBPS != 50 {
		t.Fatalf("first sample egress_bps: got %d, want %d", sample.EgressBPS, 50)
	}

	mgr.OnTrafficDelta("", 50, 150)
	mgr.takeThroughputSample(time.Unix(10, 0))

	sample, ok = mgr.ThroughputRing().Latest()
	if !ok {
		t.Fatal("expected sample in realtime ring")
	}
	if sample.IngressBPS != 10 {
		t.Fatalf("second sample ingress_bps: got %d, want %d", sample.IngressBPS, 10)
	}
	if sample.EgressBPS != 30 {
		t.Fatalf("second sample egress_bps: got %d, want %d", sample.EgressBPS, 30)
	}
}

func TestTakeSample_ConnectionsAndLeasesUseDedicatedRings(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		ConnectionsRealtimeCapacity: 8,
		ConnectionsIntervalSec:      5,
		LeasesRealtimeCapacity:      8,
		LeasesIntervalSec:           7,
		LeaseCountProvider: managerTestLeaseCountProvider{
			counts: map[string]int{"p1": 3},
		},
	})

	mgr.OnConnectionLifecycle("inbound", "open")
	mgr.OnConnectionLifecycle("outbound", "open")
	mgr.OnConnectionLifecycle("outbound", "open")

	mgr.takeConnectionsSample(time.Unix(10, 0))
	connSample, ok := mgr.ConnectionsRing().Latest()
	if !ok {
		t.Fatal("expected sample in connections ring")
	}
	if connSample.InboundConns != 1 || connSample.OutboundConns != 2 {
		t.Fatalf("connections sample mismatch: %+v", connSample)
	}

	mgr.takeLeasesSample(time.Unix(14, 0))
	leaseSample, ok := mgr.LeasesRing().Latest()
	if !ok {
		t.Fatal("expected sample in leases ring")
	}
	if leaseSample.LeasesByPlatform["p1"] != 3 {
		t.Fatalf("leases sample p1: got %d, want 3", leaseSample.LeasesByPlatform["p1"])
	}
}

func TestOnLeaseEvent_IgnoresNonPositiveLifetimeSamples(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		LeasesRealtimeCapacity: 8,
		LeasesIntervalSec:      5,
	})

	mgr.OnLeaseEvent(LeaseMetricEvent{PlatformID: "p1", Op: "remove", LifetimeNs: 0})
	mgr.OnLeaseEvent(LeaseMetricEvent{PlatformID: "p1", Op: "expire", LifetimeNs: -1})
	mgr.OnLeaseEvent(LeaseMetricEvent{PlatformID: "p1", Op: "remove", LifetimeNs: 1})
	mgr.drainLeaseLifetimeSamples()

	data := mgr.bucket.ForceFlush()
	if data == nil {
		t.Fatal("expected flushed bucket data")
	}
	acc, ok := data.LeaseLifetimes["p1"]
	if !ok {
		t.Fatal("expected p1 lease lifetime bucket")
	}
	if len(acc.Samples) != 1 || acc.Samples[0] != 1 {
		t.Fatalf("lease samples: got %+v, want [1]", acc.Samples)
	}
}

func TestFlushBucket_RetainsPendingTaskUntilRepoRecovers(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metrics.db")
	repo, err := NewMetricsRepo(dbPath)
	if err != nil {
		t.Fatalf("NewMetricsRepo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	mgr := NewManager(ManagerConfig{
		Repo:                        repo,
		LatencyBinMs:                100,
		LatencyOverflowMs:           300,
		BucketSeconds:               1,
		ThroughputRealtimeCapacity:  8,
		ThroughputIntervalSec:       1,
		ConnectionsRealtimeCapacity: 8,
		ConnectionsIntervalSec:      5,
		LeasesRealtimeCapacity:      8,
		LeasesIntervalSec:           5,
		NodePoolStats:               managerTestNodePoolStats{},
	})

	mgr.OnTrafficDelta("plat-1", 100, 200)
	mgr.OnRequestFinished(proxy.RequestFinishedEvent{
		PlatformID: "plat-1",
		NetOK:      true,
		DurationNs: int64(120 * time.Millisecond),
	})
	mgr.OnRequestFinished(proxy.RequestFinishedEvent{
		PlatformID: "plat-1",
		NetOK:      false,
		DurationNs: int64(380 * time.Millisecond),
	})

	// Force current bucket to be due for flush.
	mgr.bucket.mu.Lock()
	mgr.bucket.currentStart = time.Now().Unix() - 2
	mgr.bucket.mu.Unlock()

	if err := repo.Close(); err != nil {
		t.Fatalf("repo.Close: %v", err)
	}

	// First flush fails; task must remain pending (not discarded).
	mgr.flushBucket()
	mgr.pendingMu.Lock()
	pendingAfterFailure := len(mgr.pendingTasks)
	mgr.pendingMu.Unlock()
	if pendingAfterFailure != 1 {
		t.Fatalf("pending task count after failure: got %d, want %d", pendingAfterFailure, 1)
	}

	// Reopen DB and retry; pending task should be drained.
	recoveredRepo, err := NewMetricsRepo(dbPath)
	if err != nil {
		t.Fatalf("recover NewMetricsRepo: %v", err)
	}
	defer recoveredRepo.Close()
	mgr.repo = recoveredRepo

	mgr.flushBucket()
	mgr.pendingMu.Lock()
	pendingAfterRecover := len(mgr.pendingTasks)
	mgr.pendingMu.Unlock()
	if pendingAfterRecover != 0 {
		t.Fatalf("pending task count after recovery: got %d, want %d", pendingAfterRecover, 0)
	}

	from, to := int64(0), time.Now().Add(time.Minute).Unix()

	requestRows, err := recoveredRepo.QueryRequests(from, to, "plat-1")
	if err != nil {
		t.Fatalf("QueryRequests: %v", err)
	}
	if len(requestRows) != 1 {
		t.Fatalf("request rows len: got %d, want 1", len(requestRows))
	}
	if requestRows[0].TotalRequests != 2 || requestRows[0].SuccessRequests != 1 {
		t.Fatalf("request row mismatch: %+v", requestRows[0])
	}

	trafficRows, err := recoveredRepo.QueryTraffic(from, to, "plat-1")
	if err != nil {
		t.Fatalf("QueryTraffic: %v", err)
	}
	if len(trafficRows) != 1 {
		t.Fatalf("traffic rows len: got %d, want 1", len(trafficRows))
	}
	if trafficRows[0].IngressBytes != 100 || trafficRows[0].EgressBytes != 200 {
		t.Fatalf("traffic row mismatch: %+v", trafficRows[0])
	}

	nodePoolRows, err := recoveredRepo.QueryNodePool(from, to)
	if err != nil {
		t.Fatalf("QueryNodePool: %v", err)
	}
	if len(nodePoolRows) != 1 {
		t.Fatalf("node pool rows len: got %d, want 1", len(nodePoolRows))
	}
	if nodePoolRows[0].TotalNodes != 9 || nodePoolRows[0].HealthyNodes != 7 || nodePoolRows[0].EgressIPCount != 3 {
		t.Fatalf("node pool row mismatch: %+v", nodePoolRows[0])
	}

	globalLatencyRows, err := recoveredRepo.QueryAccessLatency(from, to, "")
	if err != nil {
		t.Fatalf("QueryAccessLatency(global): %v", err)
	}
	if len(globalLatencyRows) != 1 {
		t.Fatalf("global latency rows len: got %d, want 1", len(globalLatencyRows))
	}
	var globalBuckets []int64
	if err := json.Unmarshal([]byte(globalLatencyRows[0].BucketsJSON), &globalBuckets); err != nil {
		t.Fatalf("unmarshal global buckets: %v", err)
	}
	var globalTotal int64
	for _, c := range globalBuckets {
		globalTotal += c
	}
	if globalTotal != 2 {
		t.Fatalf("global latency sample count: got %d, want 2", globalTotal)
	}

	platformLatencyRows, err := recoveredRepo.QueryAccessLatency(from, to, "plat-1")
	if err != nil {
		t.Fatalf("QueryAccessLatency(platform): %v", err)
	}
	if len(platformLatencyRows) != 1 {
		t.Fatalf("platform latency rows len: got %d, want 1", len(platformLatencyRows))
	}
	var platformBuckets []int64
	if err := json.Unmarshal([]byte(platformLatencyRows[0].BucketsJSON), &platformBuckets); err != nil {
		t.Fatalf("unmarshal platform buckets: %v", err)
	}
	var platformTotal int64
	for _, c := range platformBuckets {
		platformTotal += c
	}
	if platformTotal != 2 {
		t.Fatalf("platform latency sample count: got %d, want 2", platformTotal)
	}
}
