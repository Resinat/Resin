package metrics

import (
	"sync"
	"time"
)

// BucketAggregator accumulates metrics within time buckets aligned to
// RESIN_METRIC_BUCKET_SECONDS boundaries. Thread-safe.
type BucketAggregator struct {
	mu            sync.Mutex
	bucketSeconds int64

	// Current bucket state (accumulated since last flush).
	currentStart int64 // bucket_start_unix
	traffic      trafficAccum
	requests     map[string]*requestAccum // platformID -> accum
	probes       probeAccum
	leaseLife    map[string]*leaseLifeAccum // platformID -> accum
}

type trafficAccum struct {
	IngressBytes int64
	EgressBytes  int64
}

type requestAccum struct {
	Total   int64
	Success int64
}

type probeAccum struct {
	Total int64
}

type leaseLifeAccum struct {
	Samples []int64 // lifetime_ns values
}

// BucketFlushData holds the accumulated data for a completed bucket.
type BucketFlushData struct {
	BucketStartUnix int64

	// Global traffic accumulation.
	Traffic trafficAccum

	// Requests per scope.
	Requests map[string]requestAccum

	// Probe count (global only).
	Probes probeAccum

	// Lease lifetime samples per platform.
	LeaseLifetimes map[string]*leaseLifeAccum
}

// NewBucketAggregator creates an aggregator with the given bucket width.
func NewBucketAggregator(bucketSeconds int) *BucketAggregator {
	if bucketSeconds <= 0 {
		bucketSeconds = 300 // 5 min default
	}
	now := time.Now().Unix()
	start := (now / int64(bucketSeconds)) * int64(bucketSeconds)
	return &BucketAggregator{
		bucketSeconds: int64(bucketSeconds),
		currentStart:  start,
		requests:      make(map[string]*requestAccum),
		leaseLife:     make(map[string]*leaseLifeAccum),
	}
}

// AddTraffic records traffic delta into the current bucket.
func (b *BucketAggregator) AddTraffic(ingress, egress int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.traffic.IngressBytes += ingress
	b.traffic.EgressBytes += egress
}

// SnapshotTraffic returns the current bucket's global traffic.
func (b *BucketAggregator) SnapshotTraffic() (bucketStartUnix, ingressBytes, egressBytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.currentStart, b.traffic.IngressBytes, b.traffic.EgressBytes
}

// CurrentBucketStartUnix returns the current in-progress bucket start timestamp.
func (b *BucketAggregator) CurrentBucketStartUnix() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentStart
}

// SnapshotRequests returns the current bucket's request counters for a scope.
// platformID="" means global scope.
func (b *BucketAggregator) SnapshotRequests(platformID string) (bucketStartUnix, total, success int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	acc, exists := b.requests[platformID]
	if !exists {
		return b.currentStart, 0, 0
	}
	return b.currentStart, acc.Total, acc.Success
}

// AddRequestCounts records aggregated request counts into the current bucket.
func (b *BucketAggregator) AddRequestCounts(platformID string, total, success int64) {
	if total <= 0 {
		return
	}
	if success < 0 {
		success = 0
	}
	if success > total {
		success = total
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	g := b.getRequest("")
	g.Total += total
	g.Success += success

	if platformID != "" {
		p := b.getRequest(platformID)
		p.Total += total
		p.Success += success
	}
}

// AddProbeCount records aggregated probe attempts.
func (b *BucketAggregator) AddProbeCount(total int64) {
	if total <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probes.Total += total
}

// SnapshotProbes returns the current bucket's global probe count.
func (b *BucketAggregator) SnapshotProbes() (bucketStartUnix, total int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentStart, b.probes.Total
}

// AddLeaseLifetime records a lease lifetime sample on removal/expiry.
func (b *BucketAggregator) AddLeaseLifetime(platformID string, lifetimeNs int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	acc, ok := b.leaseLife[platformID]
	if !ok {
		acc = &leaseLifeAccum{}
		b.leaseLife[platformID] = acc
	}
	acc.Samples = append(acc.Samples, lifetimeNs)
}

// SnapshotLeaseLifetimeSamples returns a copy of current bucket lease lifetime
// samples for a platform.
func (b *BucketAggregator) SnapshotLeaseLifetimeSamples(platformID string) (bucketStartUnix int64, samples []int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	acc, ok := b.leaseLife[platformID]
	if !ok || len(acc.Samples) == 0 {
		return b.currentStart, nil
	}
	out := make([]int64, len(acc.Samples))
	copy(out, acc.Samples)
	return b.currentStart, out
}

// MaybeFlush checks if the current time has moved past the current bucket boundary.
// If so, returns the accumulated data and resets the current bucket. Otherwise returns nil.
func (b *BucketAggregator) MaybeFlush(now time.Time) *BucketFlushData {
	b.mu.Lock()
	defer b.mu.Unlock()

	nowUnix := now.Unix()
	currentEnd := b.currentStart + b.bucketSeconds
	if nowUnix < currentEnd {
		return nil // still within current bucket
	}

	// Emit current bucket.
	data := &BucketFlushData{
		BucketStartUnix: b.currentStart,
		Traffic:         b.traffic,
		Requests:        make(map[string]requestAccum, len(b.requests)),
		Probes:          b.probes,
		LeaseLifetimes:  b.leaseLife,
	}
	for k, v := range b.requests {
		data.Requests[k] = *v
	}

	// Reset for next bucket.
	newStart := (nowUnix / b.bucketSeconds) * b.bucketSeconds
	b.currentStart = newStart
	b.traffic = trafficAccum{}
	b.requests = make(map[string]*requestAccum)
	b.probes = probeAccum{}
	b.leaseLife = make(map[string]*leaseLifeAccum)

	return data
}

// ForceFlush returns accumulated data for the current bucket (regardless of boundary)
// and resets. Used during shutdown.
func (b *BucketAggregator) ForceFlush() *BucketFlushData {
	b.mu.Lock()
	defer b.mu.Unlock()

	empty := b.traffic.IngressBytes == 0 && b.traffic.EgressBytes == 0
	if empty {
		for range b.requests {
			empty = false
			break
		}
	}
	if empty && b.probes.Total == 0 && len(b.leaseLife) == 0 {
		return nil
	}

	data := &BucketFlushData{
		BucketStartUnix: b.currentStart,
		Traffic:         b.traffic,
		Requests:        make(map[string]requestAccum, len(b.requests)),
		Probes:          b.probes,
		LeaseLifetimes:  b.leaseLife,
	}
	for k, v := range b.requests {
		data.Requests[k] = *v
	}

	b.traffic = trafficAccum{}
	b.requests = make(map[string]*requestAccum)
	b.probes = probeAccum{}
	b.leaseLife = make(map[string]*leaseLifeAccum)

	return data
}

func (b *BucketAggregator) getRequest(key string) *requestAccum {
	r, ok := b.requests[key]
	if !ok {
		r = &requestAccum{}
		b.requests[key] = r
	}
	return r
}
