package node

import (
	"math"
	"strings"
	"sync"
	"time"

	"github.com/maypok86/otter"
)

// DomainLatencyStats holds the TD-EWMA latency statistics for a single domain.
type DomainLatencyStats struct {
	Ewma        time.Duration
	LastUpdated time.Time
}

// LatencyTable is a bounded, thread-safe per-domain latency table backed by
// an otter cache. It stores DomainLatencyStats values directly with otter
// handling LRU eviction.
type LatencyTable struct {
	mu    sync.Mutex
	cache otter.Cache[string, DomainLatencyStats]
}

// NewLatencyTable creates a new LatencyTable bounded to maxEntries domains.
func NewLatencyTable(maxEntries int) *LatencyTable {
	cache, err := otter.MustBuilder[string, DomainLatencyStats](maxEntries).
		Cost(func(_ string, _ DomainLatencyStats) uint32 { return 1 }).
		Build()
	if err != nil {
		panic("node: failed to create latency table: " + err.Error())
	}
	return &LatencyTable{cache: cache}
}

// Update records a latency observation for the given domain using TD-EWMA.
// wasEmpty is true if the table had no entries before this update.
//
// TD-EWMA formula:
//
//	weight = exp(-Δt / decayWindow)
//	newEwma = oldEwma * weight + latency * (1 - weight)
//
// For the first observation of a domain, Ewma is set to the raw latency.
func (t *LatencyTable) Update(domain string, latency, decayWindow time.Duration) (wasEmpty bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	wasEmpty = t.cache.Size() == 0
	now := time.Now()

	old, found := t.cache.Get(domain)
	if !found {
		t.cache.Set(domain, DomainLatencyStats{
			Ewma:        latency,
			LastUpdated: now,
		})
		return wasEmpty
	}

	dt := now.Sub(old.LastUpdated).Seconds()
	decay := decayWindow.Seconds()
	if decay <= 0 {
		decay = 1 // prevent division by zero
	}
	weight := math.Exp(-dt / decay)
	newEwma := time.Duration(float64(old.Ewma)*weight + float64(latency)*(1-weight))

	t.cache.Set(domain, DomainLatencyStats{
		Ewma:        newEwma,
		LastUpdated: now,
	})
	return wasEmpty
}

// GetDomainStats returns the latency stats for a domain, if present.
func (t *LatencyTable) GetDomainStats(domain string) (DomainLatencyStats, bool) {
	return t.cache.Get(domain)
}

// LoadEntry stores a bootstrap-recovered entry directly (no TD-EWMA).
func (t *LatencyTable) LoadEntry(domain string, stats DomainLatencyStats) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cache.Set(domain, stats)
}

// Size returns the number of domains with latency data.
func (t *LatencyTable) Size() int {
	return t.cache.Size()
}

// Range iterates all domain entries. Returning false stops iteration.
func (t *LatencyTable) Range(fn func(domain string, stats DomainLatencyStats) bool) {
	t.cache.Range(fn)
}

// Close releases resources held by the underlying cache.
func (t *LatencyTable) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cache.Close()
}

// AverageEWMAForDomainsMs returns the average EWMA latency in milliseconds
// across domains that exist in the node's latency table.
func AverageEWMAForDomainsMs(entry *NodeEntry, domains []string) (float64, bool) {
	if entry == nil || entry.LatencyTable == nil || entry.LatencyTable.Size() == 0 || len(domains) == 0 {
		return 0, false
	}

	var sumMs float64
	var count int
	for _, domain := range domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		stats, ok := entry.LatencyTable.GetDomainStats(domain)
		if !ok {
			continue
		}
		sumMs += float64(stats.Ewma.Milliseconds())
		count++
	}
	if count == 0 {
		return 0, false
	}
	return sumMs / float64(count), true
}
