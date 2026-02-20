package metrics

import (
	"fmt"
	"log"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resin-proxy/resin/internal/proxy"
)

// RuntimeStatsProvider supplies node-pool/platform/lease/latency stats from
// the in-memory topology runtime.
type RuntimeStatsProvider interface {
	TotalNodes() int
	HealthyNodes() int
	EgressIPCount() int
	LeaseCountsByPlatform() map[string]int
	RoutableNodeCount(platformID string) (int, bool)
	PlatformEgressIPCount(platformID string) (int, bool)
	CollectNodeEWMAs(platformID string) []float64
}

// ManagerConfig configures the MetricsManager.
type ManagerConfig struct {
	Repo                        *MetricsRepo
	LatencyBinMs                int
	LatencyOverflowMs           int
	BucketSeconds               int
	ThroughputRealtimeCapacity  int
	ThroughputIntervalSec       int
	ConnectionsRealtimeCapacity int
	ConnectionsIntervalSec      int
	LeasesRealtimeCapacity      int
	LeasesIntervalSec           int
	RuntimeStats                RuntimeStatsProvider
}

// Manager is the central metrics coordinator.
// It owns the Collector, BucketAggregator, RealtimeRing, and MetricsRepo.
// Background tickers drive realtime sampling and bucket flushes.
type Manager struct {
	collector *Collector
	bucket    *BucketAggregator
	// Separate realtime rings keep per-metric sampling intervals independent.
	throughputRing  *RealtimeRing
	connectionsRing *RealtimeRing
	leasesRing      *RealtimeRing
	repo            *MetricsRepo

	runtimeStats RuntimeStatsProvider

	throughputInterval  time.Duration
	connectionsInterval time.Duration
	leasesInterval      time.Duration
	bucketSeconds       int

	// Previous cumulative byte counts for delta calculation (throughput B/s).
	prevIngressBytes int64
	prevEgressBytes  int64

	// Baselines used to derive per-bucket deltas from cumulative collector counters.
	prevBucketGlobal    bucketCounterBaseline
	prevBucketPlatforms map[string]bucketCounterBaseline
	stateMu             sync.Mutex

	// Lease lifetime samples are queued from routing hot-path and drained by
	// bucket loop to avoid lock contention in synchronous route handling.
	leaseSamplesCh      chan leaseLifetimeSample
	droppedLeaseSamples atomic.Int64

	// pendingTasks is an ordered retry queue for failed persistence writes.
	// Each task includes all writes for one bucket: primary bucket rows,
	// node-pool snapshot, and latency histograms.
	pendingMu    sync.Mutex
	pendingTasks []*persistTask

	stopCh chan struct{}
	wg     sync.WaitGroup
}

type persistTask struct {
	Bucket          *BucketFlushData
	NodePool        *nodePoolSnapshot
	GlobalLatency   []int64
	PlatformLatency map[string][]int64
}

type nodePoolSnapshot struct {
	TotalNodes    int
	HealthyNodes  int
	EgressIPCount int
}

type bucketCounterBaseline struct {
	Requests     int64
	Success      int64
	ProbeEgress  int64
	ProbeLatency int64
}

type leaseLifetimeSample struct {
	PlatformID string
	LifetimeNs int64
}

const leaseSampleQueueSize = 8192

// NewManager creates a MetricsManager.
func NewManager(cfg ManagerConfig) *Manager {
	throughputSec := cfg.ThroughputIntervalSec
	if throughputSec <= 0 {
		throughputSec = 1
	}
	connectionsSec := cfg.ConnectionsIntervalSec
	if connectionsSec <= 0 {
		connectionsSec = 5
	}
	leasesSec := cfg.LeasesIntervalSec
	if leasesSec <= 0 {
		leasesSec = 5
	}
	bucketSec := cfg.BucketSeconds
	if bucketSec <= 0 {
		bucketSec = 300
	}
	return &Manager{
		collector:           NewCollector(cfg.LatencyBinMs, cfg.LatencyOverflowMs),
		bucket:              NewBucketAggregator(bucketSec),
		throughputRing:      NewRealtimeRing(cfg.ThroughputRealtimeCapacity),
		connectionsRing:     NewRealtimeRing(cfg.ConnectionsRealtimeCapacity),
		leasesRing:          NewRealtimeRing(cfg.LeasesRealtimeCapacity),
		repo:                cfg.Repo,
		runtimeStats:        cfg.RuntimeStats,
		throughputInterval:  time.Duration(throughputSec) * time.Second,
		connectionsInterval: time.Duration(connectionsSec) * time.Second,
		leasesInterval:      time.Duration(leasesSec) * time.Second,
		bucketSeconds:       bucketSec,
		prevBucketPlatforms: make(map[string]bucketCounterBaseline),
		leaseSamplesCh:      make(chan leaseLifetimeSample, leaseSampleQueueSize),
		stopCh:              make(chan struct{}),
	}
}

// Start launches background tickers for realtime sampling and bucket flushing.
func (m *Manager) Start() {
	m.wg.Add(1)
	go m.throughputLoop()

	m.wg.Add(1)
	go m.connectionsLoop()

	m.wg.Add(1)
	go m.leasesLoop()

	m.wg.Add(1)
	go m.bucketLoop()
}

// Stop signals background workers to stop, flushes any remaining bucket data, and waits.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.wg.Wait()

	// Aggregate any final deltas into current in-memory bucket before force flush.
	m.syncCurrentBucketState()

	// Final bucket flush on shutdown (enqueue; drain below with bounded retry).
	if data := m.bucket.ForceFlush(); data != nil {
		m.enqueuePersistTask(m.buildPersistTask(data))
	}

	// Drain pending tasks with bounded retries. Failure is non-fatal.
	m.drainPendingTasks(3, 500*time.Millisecond)
}

// --- Event handlers (hot-path, called by proxy/routing/probe) ---

// OnRequestFinished records request completion metrics.
func (m *Manager) OnRequestFinished(ev proxy.RequestFinishedEvent) {
	latencyMs := ev.DurationNs / 1e6
	m.collector.RecordRequest(ev.PlatformID, ev.NetOK, latencyMs, ev.IsConnect)
}

// OnTrafficDelta records traffic bytes (implements proxy.MetricsEventSink).
func (m *Manager) OnTrafficDelta(platformID string, ingressBytes, egressBytes int64) {
	m.collector.RecordTraffic(platformID, ingressBytes, egressBytes)
	m.bucket.AddTraffic(platformID, ingressBytes, egressBytes)
}

// OnConnectionLifecycle records connection open/close (implements proxy.MetricsEventSink).
func (m *Manager) OnConnectionLifecycle(direction proxy.ConnectionDirection, op proxy.ConnectionOp) {
	var delta int64
	switch op {
	case proxy.ConnectionOpen:
		delta = 1
	case proxy.ConnectionClose:
		delta = -1
	default:
		return
	}
	m.collector.RecordConnection(direction, delta)
}

// OnProbeEvent records a probe attempt.
func (m *Manager) OnProbeEvent(ev ProbeEvent) {
	m.collector.RecordProbe(ev.Kind)
}

// OnLeaseEvent records lease lifecycle for metrics.
func (m *Manager) OnLeaseEvent(ev LeaseMetricEvent) {
	if ev.Op.HasLifetimeSample() && ev.LifetimeNs > 0 {
		select {
		case m.leaseSamplesCh <- leaseLifetimeSample{
			PlatformID: ev.PlatformID,
			LifetimeNs: ev.LifetimeNs,
		}:
		default:
			m.droppedLeaseSamples.Add(1)
		}
	}
}

// --- Query methods (for API handlers) ---

// Collector returns the underlying collector for snapshot access.
func (m *Manager) Collector() *Collector { return m.collector }

// ThroughputRing returns the realtime throughput ring buffer.
func (m *Manager) ThroughputRing() *RealtimeRing { return m.throughputRing }

// ConnectionsRing returns the realtime connections ring buffer.
func (m *Manager) ConnectionsRing() *RealtimeRing { return m.connectionsRing }

// LeasesRing returns the realtime leases ring buffer.
func (m *Manager) LeasesRing() *RealtimeRing { return m.leasesRing }

// Repo returns the metrics repo for historical queries.
func (m *Manager) Repo() *MetricsRepo { return m.repo }

// BucketSeconds returns the configured bucket duration in seconds.
func (m *Manager) BucketSeconds() int { return m.bucketSeconds }

// ThroughputIntervalSeconds returns the configured throughput realtime interval in seconds.
func (m *Manager) ThroughputIntervalSeconds() int { return int(m.throughputInterval.Seconds()) }

// ConnectionsIntervalSeconds returns the configured connections realtime interval in seconds.
func (m *Manager) ConnectionsIntervalSeconds() int { return int(m.connectionsInterval.Seconds()) }

// LeasesIntervalSeconds returns the configured leases realtime interval in seconds.
func (m *Manager) LeasesIntervalSeconds() int { return int(m.leasesInterval.Seconds()) }

// RuntimeStats returns the runtime stats provider.
func (m *Manager) RuntimeStats() RuntimeStatsProvider { return m.runtimeStats }

// SnapshotCurrentTrafficBucket returns unflushed traffic in current bucket.
// platformID="" means global scope.
func (m *Manager) SnapshotCurrentTrafficBucket(platformID string) (bucketStartUnix, ingressBytes, egressBytes int64) {
	return m.bucket.SnapshotTraffic(platformID)
}

// SnapshotCurrentRequestsBucket returns unflushed requests in current bucket.
// platformID="" means global scope.
func (m *Manager) SnapshotCurrentRequestsBucket(platformID string) (bucketStartUnix, totalRequests, successRequests int64) {
	m.syncCurrentBucketState()
	return m.bucket.SnapshotRequests(platformID)
}

// SnapshotCurrentProbeBucket returns unflushed probe count in current bucket.
func (m *Manager) SnapshotCurrentProbeBucket() (bucketStartUnix, totalCount int64) {
	m.syncCurrentBucketState()
	return m.bucket.SnapshotProbes()
}

// SnapshotCurrentAccessLatencyBucket returns the in-progress latency histogram
// for current bucket. platformID="" means global scope.
func (m *Manager) SnapshotCurrentAccessLatencyBucket(platformID string) (bucketStartUnix int64, buckets []int64) {
	bucketStartUnix = m.bucket.CurrentBucketStartUnix()

	if platformID == "" {
		snap := m.collector.Snapshot()
		return bucketStartUnix, append([]int64(nil), snap.LatencyBuckets...)
	}

	snap, ok := m.collector.PlatformSnapshot(platformID)
	if !ok {
		globalSnap := m.collector.Snapshot()
		return bucketStartUnix, make([]int64, len(globalSnap.LatencyBuckets))
	}
	return bucketStartUnix, append([]int64(nil), snap.LatencyBuckets...)
}

// SnapshotCurrentNodePoolBucket returns a node-pool snapshot for current bucket.
func (m *Manager) SnapshotCurrentNodePoolBucket() (bucketStartUnix int64, totalNodes, healthyNodes, egressIPCount int, ok bool) {
	bucketStartUnix = m.bucket.CurrentBucketStartUnix()

	if m.runtimeStats == nil {
		return bucketStartUnix, 0, 0, 0, false
	}
	return bucketStartUnix, m.runtimeStats.TotalNodes(), m.runtimeStats.HealthyNodes(), m.runtimeStats.EgressIPCount(), true
}

// SnapshotCurrentLeaseLifetimeBucket returns lease lifetime percentiles for the
// in-progress current bucket and platform.
func (m *Manager) SnapshotCurrentLeaseLifetimeBucket(platformID string) (
	bucketStartUnix int64,
	sampleCount int,
	p1Ms, p5Ms, p50Ms float64,
) {
	m.syncCurrentBucketState()
	bucketStartUnix, samples := m.bucket.SnapshotLeaseLifetimeSamples(platformID)
	if len(samples) == 0 {
		return bucketStartUnix, 0, 0, 0, 0
	}
	p1Ms, p5Ms, p50Ms = computePercentiles(samples)
	return bucketStartUnix, len(samples), p1Ms, p5Ms, p50Ms
}

// --- Background loops ---

func (m *Manager) throughputLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.throughputInterval)
	defer ticker.Stop()

	for {
		select {
		case ts := <-ticker.C:
			m.takeThroughputSample(ts)
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) connectionsLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.connectionsInterval)
	defer ticker.Stop()

	for {
		select {
		case ts := <-ticker.C:
			m.takeConnectionsSample(ts)
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) leasesLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.leasesInterval)
	defer ticker.Stop()

	for {
		select {
		case ts := <-ticker.C:
			m.takeLeasesSample(ts)
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) bucketLoop() {
	defer m.wg.Done()

	// Align the first tick to the next bucket boundary.
	// DESIGN.md: bucket_start_unix = (ts_unix / N) * N.
	now := time.Now().Unix()
	bucketSec := int64(m.bucketSeconds)
	nextBoundary := ((now / bucketSec) + 1) * bucketSec
	initialDelay := time.Duration(nextBoundary-now) * time.Second

	select {
	case <-time.After(initialDelay):
		m.flushBucket()
	case <-m.stopCh:
		return
	}

	ticker := time.NewTicker(time.Duration(m.bucketSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.flushBucket()
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) takeThroughputSample(ts time.Time) {
	snap := m.collector.Snapshot()

	// Compute per-sample delta and normalize to bytes-per-second.
	deltaIngress := snap.IngressBytes - m.prevIngressBytes
	deltaEgress := snap.EgressBytes - m.prevEgressBytes
	m.prevIngressBytes = snap.IngressBytes
	m.prevEgressBytes = snap.EgressBytes
	if deltaIngress < 0 {
		deltaIngress = 0
	}
	if deltaEgress < 0 {
		deltaEgress = 0
	}
	sampleSec := int64(m.throughputInterval / time.Second)
	if sampleSec <= 0 {
		sampleSec = 1
	}
	ingressBPS := deltaIngress / sampleSec
	egressBPS := deltaEgress / sampleSec

	m.throughputRing.Push(RealtimeSample{
		Timestamp:  ts,
		IngressBPS: ingressBPS,
		EgressBPS:  egressBPS,
	})
}

func (m *Manager) takeConnectionsSample(ts time.Time) {
	snap := m.collector.Snapshot()

	m.connectionsRing.Push(RealtimeSample{
		Timestamp:     ts,
		InboundConns:  snap.InboundConns,
		OutboundConns: snap.OutboundConns,
	})
}

func (m *Manager) takeLeasesSample(ts time.Time) {
	var leases map[string]int
	if m.runtimeStats != nil {
		leases = maps.Clone(m.runtimeStats.LeaseCountsByPlatform())
	}

	m.leasesRing.Push(RealtimeSample{
		Timestamp:        ts,
		LeasesByPlatform: leases,
	})
}

func (m *Manager) flushBucket() {
	m.syncCurrentBucketState()

	now := time.Now()
	data := m.bucket.MaybeFlush(now)
	if data != nil {
		m.enqueuePersistTask(m.buildPersistTask(data))
	}
	for {
		task, ok := m.peekPendingTask()
		if !ok {
			return
		}
		if err := m.writePersistTask(task); err != nil {
			log.Printf("[metrics] bucket persistence failed, will retry next tick: %v", err)
			return
		}
		m.popPendingTask()
	}
}

func (m *Manager) aggregateCollectorDeltasIntoBucketLocked() {
	currentGlobal := m.collector.Snapshot()
	globalBase := m.prevBucketGlobal
	globalCurrent := baselineFromSnapshot(currentGlobal)

	globalRequestsDelta := nonNegativeDelta(globalCurrent.Requests, globalBase.Requests)
	globalSuccessDelta := nonNegativeDelta(globalCurrent.Success, globalBase.Success)
	if globalSuccessDelta > globalRequestsDelta {
		globalSuccessDelta = globalRequestsDelta
	}
	globalProbeDelta := nonNegativeDelta(
		globalCurrent.ProbeEgress+globalCurrent.ProbeLatency,
		globalBase.ProbeEgress+globalBase.ProbeLatency,
	)

	currentPlatforms := m.collector.PlatformSnapshots()
	nextPlatformBaseline := make(map[string]bucketCounterBaseline, len(currentPlatforms))

	var sumPlatformRequests int64
	var sumPlatformSuccess int64

	for pid, snap := range currentPlatforms {
		cur := baselineFromSnapshot(snap)
		prev := m.prevBucketPlatforms[pid]
		nextPlatformBaseline[pid] = cur

		requestDelta := nonNegativeDelta(cur.Requests, prev.Requests)
		successDelta := nonNegativeDelta(cur.Success, prev.Success)
		if successDelta > requestDelta {
			successDelta = requestDelta
		}

		if requestDelta != 0 {
			m.bucket.AddRequestCounts(pid, requestDelta, successDelta)
		}

		sumPlatformRequests += requestDelta
		sumPlatformSuccess += successDelta
	}

	globalOnlyRequests := nonNegativeDelta(globalRequestsDelta, sumPlatformRequests)
	globalOnlySuccess := nonNegativeDelta(globalSuccessDelta, sumPlatformSuccess)
	if globalOnlySuccess > globalOnlyRequests {
		globalOnlySuccess = globalOnlyRequests
	}
	if globalOnlyRequests != 0 {
		m.bucket.AddRequestCounts("", globalOnlyRequests, globalOnlySuccess)
	}

	if globalProbeDelta != 0 {
		m.bucket.AddProbeCount(globalProbeDelta)
	}

	m.prevBucketGlobal = globalCurrent
	m.prevBucketPlatforms = nextPlatformBaseline
}

func (m *Manager) drainLeaseLifetimeSamplesLocked() {
	for {
		select {
		case sample := <-m.leaseSamplesCh:
			m.bucket.AddLeaseLifetime(sample.PlatformID, sample.LifetimeNs)
		default:
			dropped := m.droppedLeaseSamples.Swap(0)
			if dropped > 0 {
				log.Printf("[metrics] dropped %d lease lifetime samples due to full queue", dropped)
			}
			return
		}
	}
}

func (m *Manager) syncCurrentBucketState() {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	m.aggregateCollectorDeltasIntoBucketLocked()
	m.drainLeaseLifetimeSamplesLocked()
}

func baselineFromSnapshot(s CountersSnapshot) bucketCounterBaseline {
	return bucketCounterBaseline{
		Requests:     s.Requests,
		Success:      s.SuccessRequests,
		ProbeEgress:  s.ProbeEgress,
		ProbeLatency: s.ProbeLatency,
	}
}

func nonNegativeDelta(current, previous int64) int64 {
	delta := current - previous
	if delta < 0 {
		return 0
	}
	return delta
}

func (m *Manager) buildPersistTask(data *BucketFlushData) *persistTask {
	if data == nil {
		return nil
	}
	task := &persistTask{
		Bucket:          data,
		GlobalLatency:   m.collector.SwapLatencyBuckets(),
		PlatformLatency: m.collector.PlatformSwapAll(),
	}
	if m.runtimeStats != nil {
		task.NodePool = &nodePoolSnapshot{
			TotalNodes:    m.runtimeStats.TotalNodes(),
			HealthyNodes:  m.runtimeStats.HealthyNodes(),
			EgressIPCount: m.runtimeStats.EgressIPCount(),
		}
	}
	return task
}

func (m *Manager) writePersistTask(task *persistTask) error {
	if task == nil || task.Bucket == nil {
		return nil
	}
	if m.repo == nil {
		return fmt.Errorf("metrics repo is nil")
	}

	if err := m.repo.WriteBucket(task.Bucket); err != nil {
		return fmt.Errorf("write bucket: %w", err)
	}
	if task.NodePool != nil {
		if err := m.repo.WriteNodePoolSnapshot(
			task.Bucket.BucketStartUnix,
			task.NodePool.TotalNodes,
			task.NodePool.HealthyNodes,
			task.NodePool.EgressIPCount,
		); err != nil {
			return fmt.Errorf("write node pool snapshot: %w", err)
		}
	}
	if err := m.repo.WriteLatencyBucket(task.Bucket.BucketStartUnix, "", task.GlobalLatency); err != nil {
		return fmt.Errorf("write global latency bucket: %w", err)
	}
	for pid, deltas := range task.PlatformLatency {
		if err := m.repo.WriteLatencyBucket(task.Bucket.BucketStartUnix, pid, deltas); err != nil {
			return fmt.Errorf("write platform latency bucket %s: %w", pid, err)
		}
	}
	return nil
}

func (m *Manager) enqueuePersistTask(task *persistTask) {
	if task == nil {
		return
	}
	m.pendingMu.Lock()
	m.pendingTasks = append(m.pendingTasks, task)
	m.pendingMu.Unlock()
}

func (m *Manager) peekPendingTask() (*persistTask, bool) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	if len(m.pendingTasks) == 0 {
		return nil, false
	}
	return m.pendingTasks[0], true
}

func (m *Manager) popPendingTask() {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	if len(m.pendingTasks) == 0 {
		return
	}
	m.pendingTasks[0] = nil
	m.pendingTasks = m.pendingTasks[1:]
}

func (m *Manager) drainPendingTasks(maxAttempts int, retryDelay time.Duration) {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	for {
		task, ok := m.peekPendingTask()
		if !ok {
			return
		}

		success := false
		for attempt := 0; attempt < maxAttempts; attempt++ {
			if err := m.writePersistTask(task); err != nil {
				log.Printf("[metrics] shutdown persistence attempt %d failed: %v", attempt+1, err)
				if attempt+1 < maxAttempts {
					time.Sleep(retryDelay)
				}
				continue
			}
			success = true
			break
		}
		if !success {
			return
		}
		m.popPendingTask()
	}
}
