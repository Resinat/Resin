package metrics

import (
	"sync"
	"time"
)

// RealtimeSample is a single point in the realtime ring buffer.
type RealtimeSample struct {
	Timestamp     time.Time
	IngressBPS    int64
	EgressBPS     int64
	InboundConns  int64
	OutboundConns int64
	// Per-platform lease counts (snapshot from in-memory state).
	LeasesByPlatform map[string]int
}

// RealtimeRing is a fixed-size ring buffer for realtime metric samples.
type RealtimeRing struct {
	mu      sync.RWMutex
	samples []RealtimeSample
	head    int
	count   int
	cap     int
}

// NewRealtimeRing creates a ring buffer with the given capacity.
func NewRealtimeRing(capacity int) *RealtimeRing {
	if capacity <= 0 {
		capacity = 3600 // 1 hour at 1s interval
	}
	return &RealtimeRing{
		samples: make([]RealtimeSample, capacity),
		cap:     capacity,
	}
}

// Push adds a sample to the ring buffer, overwriting the oldest if full.
func (r *RealtimeRing) Push(s RealtimeSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples[r.head] = s
	r.head = (r.head + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
}

// Query returns samples within the given time range [from, to], newest first.
func (r *RealtimeRing) Query(from, to time.Time) []RealtimeSample {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []RealtimeSample
	for i := 0; i < r.count; i++ {
		idx := (r.head - 1 - i + r.cap) % r.cap
		s := r.samples[idx]
		if s.Timestamp.Before(from) {
			break // ring is chronologically ordered; stop early
		}
		if !s.Timestamp.After(to) {
			result = append(result, s)
		}
	}
	return result
}

// Latest returns the most recent sample.
func (r *RealtimeRing) Latest() (RealtimeSample, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.count == 0 {
		return RealtimeSample{}, false
	}
	idx := (r.head - 1 + r.cap) % r.cap
	return r.samples[idx], true
}
