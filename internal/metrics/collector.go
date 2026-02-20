package metrics

import (
	"sync"
	"sync/atomic"
)

// Collector holds hot-path atomic counters for global and per-platform metrics.
// All fields are updated with atomic operations for lock-free performance.
type Collector struct {
	global   *counters
	platform sync.Map // string -> *counters
}

// counters holds atomic counters for one measurement scope (global or per-platform).
type counters struct {
	requests        atomic.Int64
	successRequests atomic.Int64
	ingressBytes    atomic.Int64
	egressBytes     atomic.Int64
	inboundConns    atomic.Int64
	outboundConns   atomic.Int64
	probeEgress     atomic.Int64
	probeLatency    atomic.Int64

	// Latency histogram: fixed-bucket durations.
	// Each regular bucket[i] = count of requests with latency in
	// [i*binWidth, (i+1)*binWidth). The last bucket is overflow (>= overflowMs).
	latencyBuckets []atomic.Int64
	latencyBinMs   int
	latencyOverMs  int
}

// CountersSnapshot is a point-in-time snapshot of counters for reading.
type CountersSnapshot struct {
	Requests        int64
	SuccessRequests int64
	IngressBytes    int64
	EgressBytes     int64
	InboundConns    int64
	OutboundConns   int64
	ProbeEgress     int64
	ProbeLatency    int64
	LatencyBuckets  []int64
	LatencyBinMs    int
	LatencyOverMs   int
}

// NewCollector creates a new Collector with the given latency histogram parameters.
func NewCollector(latencyBinMs, latencyOverflowMs int) *Collector {
	if latencyBinMs <= 0 {
		latencyBinMs = 50
	}
	if latencyOverflowMs <= 0 {
		latencyOverflowMs = 5000
	}
	return &Collector{
		global: newCounters(latencyBinMs, latencyOverflowMs),
	}
}

func newCounters(binMs, overMs int) *counters {
	regularBuckets := (overMs + binMs - 1) / binMs // ceil(over/bin)
	if regularBuckets <= 0 {
		regularBuckets = 1
	}
	bucketCount := regularBuckets + 1 // +1 overflow bucket
	return &counters{
		latencyBuckets: make([]atomic.Int64, bucketCount),
		latencyBinMs:   binMs,
		latencyOverMs:  overMs,
	}
}

func (c *Collector) getOrCreatePlatform(platformID string) *counters {
	if platformID == "" {
		return nil
	}
	if v, ok := c.platform.Load(platformID); ok {
		return v.(*counters)
	}
	nc := newCounters(c.global.latencyBinMs, c.global.latencyOverMs)
	actual, _ := c.platform.LoadOrStore(platformID, nc)
	return actual.(*counters)
}

// RecordRequest records a completed request.
func (c *Collector) RecordRequest(platformID string, success bool, latencyMs int64, isConnect bool) {
	c.global.requests.Add(1)
	if success {
		c.global.successRequests.Add(1)
	}
	if !isConnect && latencyMs >= 0 {
		c.recordLatency(c.global, latencyMs)
	}

	if pc := c.getOrCreatePlatform(platformID); pc != nil {
		pc.requests.Add(1)
		if success {
			pc.successRequests.Add(1)
		}
		if !isConnect && latencyMs >= 0 {
			c.recordLatency(pc, latencyMs)
		}
	}
}

func (c *Collector) recordLatency(ct *counters, ms int64) {
	overflowIdx := len(ct.latencyBuckets) - 1
	if overflowIdx < 0 {
		return
	}

	// Overflow bucket counts samples >= overflow_ms.
	if ms >= int64(ct.latencyOverMs) {
		ct.latencyBuckets[overflowIdx].Add(1)
		return
	}

	// Regular buckets are [lower, upper) with bin width.
	idx := 0
	if ms >= 0 {
		idx = int(ms / int64(ct.latencyBinMs))
	}
	if idx >= overflowIdx {
		idx = overflowIdx - 1
	}
	if idx < 0 {
		idx = 0
	}

	ct.latencyBuckets[idx].Add(1)
}

// RecordTraffic records traffic bytes.
func (c *Collector) RecordTraffic(platformID string, ingress, egress int64) {
	c.global.ingressBytes.Add(ingress)
	c.global.egressBytes.Add(egress)
	if pc := c.getOrCreatePlatform(platformID); pc != nil {
		pc.ingressBytes.Add(ingress)
		pc.egressBytes.Add(egress)
	}
}

// RecordConnection records a connection lifecycle event.
func (c *Collector) RecordConnection(dir ConnectionDirection, delta int64) {
	if dir == ConnInbound {
		c.global.inboundConns.Add(delta)
	} else {
		c.global.outboundConns.Add(delta)
	}
}

// RecordProbe records a probe attempt.
func (c *Collector) RecordProbe(kind ProbeKind) {
	switch kind {
	case ProbeKindEgress:
		c.global.probeEgress.Add(1)
	case ProbeKindLatency:
		c.global.probeLatency.Add(1)
	}
}

// Snapshot returns a point-in-time snapshot of the global counters.
func (c *Collector) Snapshot() CountersSnapshot {
	return snapshot(c.global)
}

// PlatformSnapshot returns a snapshot for a specific platform.
func (c *Collector) PlatformSnapshot(platformID string) (CountersSnapshot, bool) {
	v, ok := c.platform.Load(platformID)
	if !ok {
		return CountersSnapshot{}, false
	}
	return snapshot(v.(*counters)), true
}

// PlatformSnapshots returns snapshots for all known platforms.
func (c *Collector) PlatformSnapshots() map[string]CountersSnapshot {
	result := make(map[string]CountersSnapshot)
	c.platform.Range(func(key, value any) bool {
		result[key.(string)] = snapshot(value.(*counters))
		return true
	})
	return result
}

func snapshot(ct *counters) CountersSnapshot {
	s := CountersSnapshot{
		Requests:        ct.requests.Load(),
		SuccessRequests: ct.successRequests.Load(),
		IngressBytes:    ct.ingressBytes.Load(),
		EgressBytes:     ct.egressBytes.Load(),
		InboundConns:    ct.inboundConns.Load(),
		OutboundConns:   ct.outboundConns.Load(),
		ProbeEgress:     ct.probeEgress.Load(),
		ProbeLatency:    ct.probeLatency.Load(),
		LatencyBuckets:  make([]int64, len(ct.latencyBuckets)),
		LatencyBinMs:    ct.latencyBinMs,
		LatencyOverMs:   ct.latencyOverMs,
	}
	for i := range ct.latencyBuckets {
		s.LatencyBuckets[i] = ct.latencyBuckets[i].Load()
	}
	return s
}

// swapLatencyBuckets atomically swaps each bucket counter to 0
// and returns the deltas since last swap.
func swapLatencyBuckets(ct *counters) []int64 {
	deltas := make([]int64, len(ct.latencyBuckets))
	for i := range ct.latencyBuckets {
		deltas[i] = ct.latencyBuckets[i].Swap(0)
	}
	return deltas
}

// SwapLatencyBuckets atomically drains the global latency histogram, returning
// per-bucket counts accumulated since the last call. The counters are reset to 0
// so the next call only captures new samples.
func (c *Collector) SwapLatencyBuckets() []int64 {
	return swapLatencyBuckets(c.global)
}

// PlatformSwapLatencyBuckets does the same for a specific platform.
func (c *Collector) PlatformSwapLatencyBuckets(platformID string) ([]int64, bool) {
	v, ok := c.platform.Load(platformID)
	if !ok {
		return nil, false
	}
	return swapLatencyBuckets(v.(*counters)), true
}

// PlatformSwapAll atomically drains latency histograms for all known platforms.
func (c *Collector) PlatformSwapAll() map[string][]int64 {
	result := make(map[string][]int64)
	c.platform.Range(func(key, value any) bool {
		result[key.(string)] = swapLatencyBuckets(value.(*counters))
		return true
	})
	return result
}
