package probe

import (
	"log"
	"sync"
	"time"

	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/scanloop"
	"github.com/resin-proxy/resin/internal/topology"
	"github.com/sagernet/sing-box/adapter"
)

// Fetcher executes an HTTP request through a node's outbound, returning the
// response body and TLS handshake latency. This is injectable for testing.
type Fetcher func(outbound adapter.Outbound, url string) (body []byte, latency time.Duration, err error)

// ProbeConfig configures the ProbeManager.
// Field names align 1:1 with RuntimeConfig to prevent mis-wiring.
type ProbeConfig struct {
	Pool        *topology.GlobalNodePool
	Concurrency int // max concurrent probes

	// Fetcher executes HTTP via outbound. Injectable for testing.
	Fetcher Fetcher

	// Interval thresholds — closures for hot-reload from RuntimeConfig.
	MaxEgressTestInterval           func() time.Duration
	MaxLatencyTestInterval          func() time.Duration
	MaxAuthorityLatencyTestInterval func() time.Duration

	LatencyTestURL     func() string
	LatencyAuthorities func() []string
}

// ProbeManager schedules and executes active probes against nodes in the pool.
// It holds a direct reference to *topology.GlobalNodePool (no interface).
type ProbeManager struct {
	pool    *topology.GlobalNodePool
	sem     chan struct{}
	stopCh  chan struct{}
	wg      sync.WaitGroup
	fetcher Fetcher

	maxEgressTestInterval           func() time.Duration
	maxLatencyTestInterval          func() time.Duration
	maxAuthorityLatencyTestInterval func() time.Duration
	latencyTestURL                  func() string
	latencyAuthorities              func() []string
}

// NewProbeManager creates a new ProbeManager.
func NewProbeManager(cfg ProbeConfig) *ProbeManager {
	conc := cfg.Concurrency
	if conc <= 0 {
		conc = 8
	}
	return &ProbeManager{
		pool:                            cfg.Pool,
		sem:                             make(chan struct{}, conc),
		stopCh:                          make(chan struct{}),
		fetcher:                         cfg.Fetcher,
		maxEgressTestInterval:           cfg.MaxEgressTestInterval,
		maxLatencyTestInterval:          cfg.MaxLatencyTestInterval,
		maxAuthorityLatencyTestInterval: cfg.MaxAuthorityLatencyTestInterval,
		latencyTestURL:                  cfg.LatencyTestURL,
		latencyAuthorities:              cfg.LatencyAuthorities,
	}
}

// Start launches the background probe workers.
func (m *ProbeManager) Start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		scanloop.Run(m.stopCh, scanloop.DefaultMinInterval, scanloop.DefaultJitterRange, m.scanEgress)
	}()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		scanloop.Run(m.stopCh, scanloop.DefaultMinInterval, scanloop.DefaultJitterRange, m.scanLatency)
	}()
}

// Stop signals all probe workers to stop and waits for completion.
//
// Design note:
//   - Immediate probes are accounted in wg, so in-flight TriggerImmediateEgressProbe
//     work is drained before Stop returns.
//   - We intentionally do not add extra lifecycle state (e.g. stopping flag/mutex)
//     to reject post-stop triggers here. Expected ownership is that callers stop
//     upstream schedulers/event sources before calling Stop.
func (m *ProbeManager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

// TriggerImmediateEgressProbe fires an async egress probe for a node.
// The goroutine waits for a semaphore slot (or stop signal), never drops.
// Caller returns immediately.
//
// Design note:
//   - This method always increments wg so Stop can wait for any in-flight
//     immediate probe goroutine.
//   - Stop-trigger ordering is a caller contract; this method does not enforce
//     a "reject after stop" policy with additional manager-global state.
//   - Tradeoff: we spawn first, then acquire sem in the goroutine. This keeps
//     callers non-blocking and preserves "never drop" semantics. Under bursty
//     triggers, waiting goroutine count may rise, but actual outbound probe
//     concurrency is still hard-limited by sem capacity.
func (m *ProbeManager) TriggerImmediateEgressProbe(hash node.Hash) {
	m.wg.Add(1)

	go func() {
		defer m.wg.Done()
		select {
		case m.sem <- struct{}{}:
			defer func() { <-m.sem }()
		case <-m.stopCh:
			return // shutting down
		}

		entry, ok := m.pool.GetEntry(hash)
		if !ok {
			return
		}
		if entry.Outbound.Load() == nil {
			return // nil outbound → skip
		}

		m.probeEgress(hash, entry)
	}()
}

// scanEgress iterates all pool nodes and probes those due for egress check.
func (m *ProbeManager) scanEgress() {
	now := time.Now()
	interval := 24 * time.Hour // default MaxEgressTestInterval
	if m.maxEgressTestInterval != nil {
		interval = m.maxEgressTestInterval()
	}
	lookahead := 15 * time.Second

	m.pool.Range(func(h node.Hash, entry *node.NodeEntry) bool {
		// Check stop signal.
		select {
		case <-m.stopCh:
			return false
		default:
		}

		if entry.Outbound.Load() == nil {
			return true // skip nil outbound
		}

		// Check if due: lastCheck + interval - lookahead <= now.
		lastCheck := entry.LastEgressUpdate.Load()
		if lastCheck > 0 {
			nextDue := time.Unix(0, lastCheck).Add(interval).Add(-lookahead)
			if now.Before(nextDue) {
				return true // not yet due
			}
		}

		// Acquire sem or skip on shutdown.
		select {
		case m.sem <- struct{}{}:
		case <-m.stopCh:
			return false
		}

		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer func() { <-m.sem }()
			m.probeEgress(h, entry)
		}()

		return true
	})
}

// scanLatency iterates all pool nodes and probes those due for latency check.
func (m *ProbeManager) scanLatency() {
	now := time.Now()
	maxLatencyInterval := 5 * time.Minute // default
	if m.maxLatencyTestInterval != nil {
		maxLatencyInterval = m.maxLatencyTestInterval()
	}
	maxAuthorityInterval := 1 * time.Hour // default
	if m.maxAuthorityLatencyTestInterval != nil {
		maxAuthorityInterval = m.maxAuthorityLatencyTestInterval()
	}
	lookahead := 15 * time.Second
	testURL := "https://www.gstatic.com/generate_204"
	if m.latencyTestURL != nil {
		testURL = m.latencyTestURL()
	}
	var authorities []string
	if m.latencyAuthorities != nil {
		authorities = m.latencyAuthorities()
	}

	m.pool.Range(func(h node.Hash, entry *node.NodeEntry) bool {
		select {
		case <-m.stopCh:
			return false
		default:
		}

		if entry.Outbound.Load() == nil {
			return true // skip nil outbound
		}

		if !m.isLatencyProbeDue(entry, now, maxLatencyInterval, maxAuthorityInterval, authorities, lookahead) {
			return true
		}

		// Acquire sem or skip on shutdown.
		select {
		case m.sem <- struct{}{}:
		case <-m.stopCh:
			return false
		}

		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer func() { <-m.sem }()
			m.probeLatency(h, entry, testURL)
		}()

		return true
	})
}

// isLatencyProbeDue checks whether a node needs a latency probe.
// Returns true if:
//   - the node has no recent latency record (within MaxLatencyTestInterval), OR
//   - no authority domain has a recent record (within MaxAuthorityLatencyTestInterval).
func (m *ProbeManager) isLatencyProbeDue(
	entry *node.NodeEntry,
	now time.Time,
	maxLatencyInterval, maxAuthorityInterval time.Duration,
	authorities []string,
	lookahead time.Duration,
) bool {
	if entry.LatencyTable == nil || entry.LatencyTable.Size() == 0 {
		return true // no entries at all
	}

	// Check if any entry is recent enough.
	hasRecentAny := false
	entry.LatencyTable.Range(func(_ string, stats node.DomainLatencyStats) bool {
		deadline := stats.LastUpdated.Add(maxLatencyInterval).Add(-lookahead)
		if now.Before(deadline) {
			hasRecentAny = true
			return false // found recent, stop
		}
		return true
	})
	if !hasRecentAny {
		return true // all entries are stale
	}

	// Check if any authority domain has a recent record.
	if len(authorities) == 0 {
		return false // no authorities configured — any recent record is enough
	}
	for _, auth := range authorities {
		stats, ok := entry.LatencyTable.GetDomainStats(auth)
		if ok {
			deadline := stats.LastUpdated.Add(maxAuthorityInterval).Add(-lookahead)
			if now.Before(deadline) {
				return false // found recent authority record
			}
		}
	}
	return true // no recent authority record
}

// probeEgress performs a single egress probe against a node via Cloudflare trace.
// Writes back: RecordResult, RecordLatency (cloudflare.com), UpdateNodeEgressIP.
func (m *ProbeManager) probeEgress(hash node.Hash, entry *node.NodeEntry) {
	if m.fetcher == nil {
		return
	}

	outboundPtr := entry.Outbound.Load()
	if outboundPtr == nil {
		return
	}

	body, latency, err := m.fetcher(*outboundPtr, "https://cloudflare.com/cdn-cgi/trace")
	if err != nil {
		m.pool.RecordResult(hash, false)
		log.Printf("[probe] egress probe failed for %s: %v", hash.Hex(), err)
		return
	}

	// Success.
	m.pool.RecordResult(hash, true)

	// Ignore non-positive latency samples to avoid polluting EWMA.
	if latency > 0 {
		m.pool.RecordLatency(hash, "cloudflare.com", latency)
	}

	// Parse egress IP from trace response.
	ip, err := ParseCloudflareTrace(body)
	if err != nil {
		// Intentionally do NOT touch LastEgressUpdate on parse failure.
		// LastEgressUpdate means "last successful egress-IP sample"; keeping it
		// unchanged keeps this node due for near-term retry in the scan loop.
		log.Printf("[probe] parse egress IP for %s: %v", hash.Hex(), err)
		return
	}
	m.pool.UpdateNodeEgressIP(hash, ip)
}

// probeLatency performs a latency probe against a node using the configured test URL.
// Writes back: RecordResult, RecordLatency.
func (m *ProbeManager) probeLatency(hash node.Hash, entry *node.NodeEntry, testURL string) {
	if m.fetcher == nil {
		return
	}

	outboundPtr := entry.Outbound.Load()
	if outboundPtr == nil {
		return
	}

	_, latency, err := m.fetcher(*outboundPtr, testURL)
	if err != nil {
		m.pool.RecordResult(hash, false)
		log.Printf("[probe] latency probe failed for %s: %v", hash.Hex(), err)
		return
	}

	m.pool.RecordResult(hash, true)
	if latency > 0 {
		m.pool.RecordLatency(hash, testURL, latency)
	}
}
