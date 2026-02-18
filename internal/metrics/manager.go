package metrics

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resin-proxy/resin/internal/proxy"
)

// NodePoolStatsProvider supplies pool-level statistics for periodic snapshots.
type NodePoolStatsProvider interface {
	TotalNodes() int
	HealthyNodes() int
	EgressIPCount() int
}

// LeaseCountProvider supplies per-platform lease counts for realtime sampling.
type LeaseCountProvider interface {
	LeaseCountsByPlatform() map[string]int
}

// PlatformStatsProvider supplies per-platform node statistics for snapshot endpoints.
type PlatformStatsProvider interface {
	RoutableNodeCount(platformID string) (int, bool)
	PlatformEgressIPCount(platformID string) (int, bool)
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
	NodePoolStats               NodePoolStatsProvider
	LeaseCountProvider          LeaseCountProvider
	PlatformStats               PlatformStatsProvider
	NodeLatency                 NodeLatencyProvider
}

// NodeLatencyProvider supplies per-node authority-domain EWMA latencies for
// the /metrics/snapshots/node-latency-distribution endpoint.
type NodeLatencyProvider interface {
	// CollectNodeEWMAs returns a list of per-node EWMA millisecond values
	// for the configured authority domains. If platformID is empty, returns
	// all nodes; otherwise only nodes routable by that platform.
	CollectNodeEWMAs(platformID string) []float64
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

	nodePoolStats      NodePoolStatsProvider
	leaseCountProvider LeaseCountProvider
	platformStats      PlatformStatsProvider
	nodeLatency        NodeLatencyProvider

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
	IngressBytes int64
	EgressBytes  int64
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
		nodePoolStats:       cfg.NodePoolStats,
		leaseCountProvider:  cfg.LeaseCountProvider,
		platformStats:       cfg.PlatformStats,
		nodeLatency:         cfg.NodeLatency,
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
	m.aggregateCollectorDeltasIntoBucket()
	m.drainLeaseLifetimeSamples()

	// Final bucket flush on shutdown (enqueue; drain below with bounded retry).
	if data := m.bucket.ForceFlush(); data != nil {
		m.enqueuePersistTask(m.buildPersistTask(data))
	}

	// Drain pending tasks with bounded retries. Failure is non-fatal.
	m.drainPendingTasks(3, 500*time.Millisecond)
}

// --- Event handlers (hot-path, called by proxy/routing/probe) ---

// OnRequestFinished implements the metrics side of proxy.EventEmitter.
func (m *Manager) OnRequestFinished(ev proxy.RequestFinishedEvent) {
	latencyMs := ev.DurationNs / 1e6
	m.collector.RecordRequest(ev.PlatformID, ev.NetOK, latencyMs, ev.IsConnect)
}

// EmitRequestFinished implements proxy.EventEmitter.
func (m *Manager) EmitRequestFinished(ev proxy.RequestFinishedEvent) {
	m.OnRequestFinished(ev)
}

// EmitRequestLog is a no-op for Manager (handled by requestlog.Service).
func (m *Manager) EmitRequestLog(proxy.RequestLogEntry) {}

// OnTrafficDelta records traffic bytes (implements proxy.MetricsEventSink).
func (m *Manager) OnTrafficDelta(platformID string, ingressBytes, egressBytes int64) {
	m.collector.RecordTraffic(platformID, ingressBytes, egressBytes)
}

// OnConnectionLifecycle records connection open/close (implements proxy.MetricsEventSink).
// direction: "inbound" or "outbound"; op: "open" or "close".
func (m *Manager) OnConnectionLifecycle(direction, op string) {
	var delta int64 = 1
	if op == "close" {
		delta = -1
	}
	var dir ConnectionDirection
	if direction == "inbound" {
		dir = ConnInbound
	} else {
		dir = ConnOutbound
	}
	m.collector.RecordConnection(dir, delta)
}

// OnProbeEvent records a probe attempt.
func (m *Manager) OnProbeEvent(ev ProbeEvent) {
	m.collector.RecordProbe(ev.Kind)
}

// OnLeaseEvent records lease lifecycle for metrics.
func (m *Manager) OnLeaseEvent(ev LeaseMetricEvent) {
	if (ev.Op == "remove" || ev.Op == "expire") && ev.LifetimeNs > 0 {
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

// Ring returns the realtime throughput ring buffer.
// Deprecated: use ThroughputRing/ConnectionsRing/LeasesRing.
func (m *Manager) Ring() *RealtimeRing { return m.throughputRing }

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

// SampleIntervalSeconds returns the configured throughput realtime interval in seconds.
// Deprecated: use ThroughputIntervalSeconds/ConnectionsIntervalSeconds/LeasesIntervalSeconds.
func (m *Manager) SampleIntervalSeconds() int { return m.ThroughputIntervalSeconds() }

// NodePoolStats returns the node pool stats provider.
func (m *Manager) NodePoolStats() NodePoolStatsProvider { return m.nodePoolStats }

// PlatformStats returns the platform stats provider.
func (m *Manager) PlatformStats() PlatformStatsProvider { return m.platformStats }

// NodeLatency returns the node latency provider.
func (m *Manager) NodeLatency() NodeLatencyProvider { return m.nodeLatency }

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
	if m.leaseCountProvider != nil {
		leases = cloneLeaseCounts(m.leaseCountProvider.LeaseCountsByPlatform())
	}

	m.leasesRing.Push(RealtimeSample{
		Timestamp:        ts,
		LeasesByPlatform: leases,
	})
}

func (m *Manager) takeSample() {
	ts := time.Now()
	m.takeThroughputSample(ts)
	m.takeConnectionsSample(ts)
	m.takeLeasesSample(ts)
}

func (m *Manager) flushBucket() {
	m.aggregateCollectorDeltasIntoBucket()
	m.drainLeaseLifetimeSamples()

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

func (m *Manager) aggregateCollectorDeltasIntoBucket() {
	currentGlobal := m.collector.Snapshot()
	globalBase := m.prevBucketGlobal
	globalCurrent := baselineFromSnapshot(currentGlobal)

	globalIngressDelta := nonNegativeDelta(globalCurrent.IngressBytes, globalBase.IngressBytes)
	globalEgressDelta := nonNegativeDelta(globalCurrent.EgressBytes, globalBase.EgressBytes)
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

	var sumPlatformIngress int64
	var sumPlatformEgress int64
	var sumPlatformRequests int64
	var sumPlatformSuccess int64

	for pid, snap := range currentPlatforms {
		cur := baselineFromSnapshot(snap)
		prev := m.prevBucketPlatforms[pid]
		nextPlatformBaseline[pid] = cur

		ingressDelta := nonNegativeDelta(cur.IngressBytes, prev.IngressBytes)
		egressDelta := nonNegativeDelta(cur.EgressBytes, prev.EgressBytes)
		requestDelta := nonNegativeDelta(cur.Requests, prev.Requests)
		successDelta := nonNegativeDelta(cur.Success, prev.Success)
		if successDelta > requestDelta {
			successDelta = requestDelta
		}

		if ingressDelta != 0 || egressDelta != 0 {
			m.bucket.AddTraffic(pid, ingressDelta, egressDelta)
		}
		if requestDelta != 0 {
			m.bucket.AddRequestCounts(pid, requestDelta, successDelta)
		}

		sumPlatformIngress += ingressDelta
		sumPlatformEgress += egressDelta
		sumPlatformRequests += requestDelta
		sumPlatformSuccess += successDelta
	}

	// Account for any global-only events not attributed to a platform.
	globalOnlyIngress := nonNegativeDelta(globalIngressDelta, sumPlatformIngress)
	globalOnlyEgress := nonNegativeDelta(globalEgressDelta, sumPlatformEgress)
	if globalOnlyIngress != 0 || globalOnlyEgress != 0 {
		m.bucket.AddTraffic("", globalOnlyIngress, globalOnlyEgress)
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

func (m *Manager) drainLeaseLifetimeSamples() {
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

func baselineFromSnapshot(s CountersSnapshot) bucketCounterBaseline {
	return bucketCounterBaseline{
		Requests:     s.Requests,
		Success:      s.SuccessRequests,
		IngressBytes: s.IngressBytes,
		EgressBytes:  s.EgressBytes,
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

func cloneLeaseCounts(src map[string]int) map[string]int {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]int, len(src))
	for platformID, count := range src {
		dst[platformID] = count
	}
	return dst
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
	if m.nodePoolStats != nil {
		task.NodePool = &nodePoolSnapshot{
			TotalNodes:    m.nodePoolStats.TotalNodes(),
			HealthyNodes:  m.nodePoolStats.HealthyNodes(),
			EgressIPCount: m.nodePoolStats.EgressIPCount(),
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
