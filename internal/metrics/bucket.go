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
	currentStart int64                    // bucket_start_unix
	traffic      map[string]*trafficAccum // platformID -> accum (empty-string key = global)
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

	// Traffic per scope (platformID="" is global).
	Traffic map[string]trafficAccum

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
		traffic:       make(map[string]*trafficAccum),
		requests:      make(map[string]*requestAccum),
		leaseLife:     make(map[string]*leaseLifeAccum),
	}
}

// AddTraffic records traffic delta into the current bucket.
func (b *BucketAggregator) AddTraffic(platformID string, ingress, egress int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Global.
	g := b.getTraffic("")
	g.IngressBytes += ingress
	g.EgressBytes += egress

	// Per-platform.
	if platformID != "" {
		p := b.getTraffic(platformID)
		p.IngressBytes += ingress
		p.EgressBytes += egress
	}
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

// AddRequest records a completed request into the current bucket.
func (b *BucketAggregator) AddRequest(platformID string, success bool) {
	successCount := int64(0)
	if success {
		successCount = 1
	}
	b.AddRequestCounts(platformID, 1, successCount)
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

// AddProbe records a probe attempt.
func (b *BucketAggregator) AddProbe() {
	b.AddProbeCount(1)
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
		Traffic:         make(map[string]trafficAccum, len(b.traffic)),
		Requests:        make(map[string]requestAccum, len(b.requests)),
		Probes:          b.probes,
		LeaseLifetimes:  b.leaseLife,
	}
	for k, v := range b.traffic {
		data.Traffic[k] = *v
	}
	for k, v := range b.requests {
		data.Requests[k] = *v
	}

	// Reset for next bucket.
	newStart := (nowUnix / b.bucketSeconds) * b.bucketSeconds
	b.currentStart = newStart
	b.traffic = make(map[string]*trafficAccum)
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

	empty := true
	for range b.traffic {
		empty = false
		break
	}
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
		Traffic:         make(map[string]trafficAccum, len(b.traffic)),
		Requests:        make(map[string]requestAccum, len(b.requests)),
		Probes:          b.probes,
		LeaseLifetimes:  b.leaseLife,
	}
	for k, v := range b.traffic {
		data.Traffic[k] = *v
	}
	for k, v := range b.requests {
		data.Requests[k] = *v
	}

	b.traffic = make(map[string]*trafficAccum)
	b.requests = make(map[string]*requestAccum)
	b.probes = probeAccum{}
	b.leaseLife = make(map[string]*leaseLifeAccum)

	return data
}

func (b *BucketAggregator) getTraffic(key string) *trafficAccum {
	t, ok := b.traffic[key]
	if !ok {
		t = &trafficAccum{}
		b.traffic[key] = t
	}
	return t
}

func (b *BucketAggregator) getRequest(key string) *requestAccum {
	r, ok := b.requests[key]
	if !ok {
		r = &requestAccum{}
		b.requests[key] = r
	}
	return r
}
