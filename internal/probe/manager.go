package probe

import (
	"fmt"
	"log"
	"net/netip"
	"sync"
	"time"

	"github.com/resin-proxy/resin/internal/netutil"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/scanloop"
	"github.com/resin-proxy/resin/internal/topology"
)

// Fetcher executes an HTTP request through the given node, returning
// response body and TLS handshake latency. This is injectable for testing.
type Fetcher func(hash node.Hash, url string) (body []byte, latency time.Duration, err error)

// ProbeConfig configures the ProbeManager.
// Field names align 1:1 with RuntimeConfig to prevent mis-wiring.
type ProbeConfig struct {
	Pool        *topology.GlobalNodePool
	Concurrency int // max concurrent probes

	// Fetcher executes HTTP via node hash. Injectable for testing.
	Fetcher Fetcher

	// Interval thresholds — closures for hot-reload from RuntimeConfig.
	MaxEgressTestInterval           func() time.Duration
	MaxLatencyTestInterval          func() time.Duration
	MaxAuthorityLatencyTestInterval func() time.Duration

	LatencyTestURL     func() string
	LatencyAuthorities func() []string

	// OnProbeEvent is called after each probe attempt completes (egress or latency).
	// The kind parameter is "egress" or "latency".
	OnProbeEvent func(kind string)
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
	onProbeEvent                    func(kind string)
}

const (
	egressTraceURL        = "https://cloudflare.com/cdn-cgi/trace"
	egressTraceDomain     = "cloudflare.com"
	defaultLatencyTestURL = "https://www.gstatic.com/generate_204"
)

type egressProbeErrorStage int

const (
	egressProbeNoError egressProbeErrorStage = iota
	egressProbeFetchError
	egressProbeParseError
)

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
		onProbeEvent:                    cfg.OnProbeEvent,
	}
}

// SetOnProbeEvent sets the probe event callback. Must be called before Start.
func (m *ProbeManager) SetOnProbeEvent(fn func(kind string)) {
	m.onProbeEvent = fn
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

// EgressProbeResult holds the results of a synchronous egress probe.
type EgressProbeResult struct {
	EgressIP      string  `json:"egress_ip"`
	Region        string  `json:"region,omitempty"`
	LatencyEwmaMs float64 `json:"latency_ewma_ms"`
}

// ProbeEgressSync performs a blocking egress probe and returns the results.
// Used by API action endpoints that must return probe data synchronously.
func (m *ProbeManager) ProbeEgressSync(hash node.Hash) (*EgressProbeResult, error) {
	if m.fetcher == nil {
		return nil, fmt.Errorf("no probe fetcher configured")
	}

	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return nil, fmt.Errorf("node not found")
	}
	if entry.Outbound.Load() == nil {
		return nil, fmt.Errorf("node outbound not ready")
	}

	// Acquire semaphore (blocking, with stop-signal awareness).
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-m.stopCh:
		return nil, fmt.Errorf("probe manager stopped")
	}

	// Record synchronous probe attempts for metrics parity with async paths.
	if m.onProbeEvent != nil {
		m.onProbeEvent("egress")
	}

	ip, stage, err := m.performEgressProbe(hash)
	if err != nil {
		if stage == egressProbeParseError {
			return nil, fmt.Errorf("parse egress IP: %w", err)
		}
		return nil, fmt.Errorf("egress probe failed: %w", err)
	}

	// Read back EWMA for cloudflare.com from the latency table.
	var ewmaMs float64
	if entry.LatencyTable != nil {
		if stats, ok := entry.LatencyTable.GetDomainStats(egressTraceDomain); ok {
			ewmaMs = float64(stats.Ewma) / float64(time.Millisecond)
		}
	}

	return &EgressProbeResult{
		EgressIP:      ip.String(),
		LatencyEwmaMs: ewmaMs,
	}, nil
}

// LatencyProbeResult holds the results of a synchronous latency probe.
type LatencyProbeResult struct {
	LatencyEwmaMs float64 `json:"latency_ewma_ms"`
}

// ProbeLatencySync performs a blocking latency probe and returns the results.
func (m *ProbeManager) ProbeLatencySync(hash node.Hash) (*LatencyProbeResult, error) {
	if m.fetcher == nil {
		return nil, fmt.Errorf("no probe fetcher configured")
	}

	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return nil, fmt.Errorf("node not found")
	}
	if entry.Outbound.Load() == nil {
		return nil, fmt.Errorf("node outbound not ready")
	}

	testURL := m.currentLatencyTestURL()
	domain := netutil.ExtractDomain(testURL)

	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-m.stopCh:
		return nil, fmt.Errorf("probe manager stopped")
	}

	// Record synchronous probe attempts for metrics parity with async paths.
	if m.onProbeEvent != nil {
		m.onProbeEvent("latency")
	}

	if err := m.performLatencyProbe(hash, testURL); err != nil {
		return nil, fmt.Errorf("latency probe failed: %w", err)
	}

	// Read back EWMA.
	var ewmaMs float64
	if entry.LatencyTable != nil {
		if stats, ok := entry.LatencyTable.GetDomainStats(domain); ok {
			ewmaMs = float64(stats.Ewma) / float64(time.Millisecond)
		}
	}

	return &LatencyProbeResult{
		LatencyEwmaMs: ewmaMs,
	}, nil
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

		// Check if due: lastAttempt + interval - lookahead <= now.
		lastCheck := entry.LastEgressUpdateAttempt.Load()
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
	testURL := m.currentLatencyTestURL()
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

// isLatencyProbeDue checks whether a node needs a latency probe, based on
// last probe-attempt timestamps (not latency-table timestamps).
func (m *ProbeManager) isLatencyProbeDue(
	entry *node.NodeEntry,
	now time.Time,
	maxLatencyInterval, maxAuthorityInterval time.Duration,
	authorities []string,
	lookahead time.Duration,
) bool {
	lastAny := entry.LastLatencyProbeAttempt.Load()
	if lastAny == 0 {
		return true
	}
	anyDeadline := time.Unix(0, lastAny).Add(maxLatencyInterval).Add(-lookahead)
	if !now.Before(anyDeadline) {
		return true
	}

	if len(authorities) == 0 {
		return false
	}

	lastAuthority := entry.LastAuthorityLatencyProbeAttempt.Load()
	if lastAuthority == 0 {
		return true
	}
	authorityDeadline := time.Unix(0, lastAuthority).Add(maxAuthorityInterval).Add(-lookahead)
	return !now.Before(authorityDeadline)
}

// probeEgress performs a single egress probe against a node via Cloudflare trace.
// Writes back: RecordResult, RecordLatency (cloudflare.com), UpdateNodeEgressIP.
func (m *ProbeManager) probeEgress(hash node.Hash, entry *node.NodeEntry) {
	if m.fetcher == nil {
		return
	}

	if entry.Outbound.Load() == nil {
		return
	}

	// Always record the probe attempt (success or failure).
	if m.onProbeEvent != nil {
		m.onProbeEvent("egress")
	}

	_, stage, err := m.performEgressProbe(hash)
	if err != nil {
		if stage == egressProbeParseError {
			log.Printf("[probe] parse egress IP for %s: %v", hash.Hex(), err)
			return
		}
		log.Printf("[probe] egress probe failed for %s: %v", hash.Hex(), err)
		return
	}
}

// probeLatency performs a latency probe against a node using the configured test URL.
// Writes back: RecordResult, RecordLatency.
func (m *ProbeManager) probeLatency(hash node.Hash, entry *node.NodeEntry, testURL string) {
	if m.fetcher == nil {
		return
	}

	if entry.Outbound.Load() == nil {
		return
	}

	// Always record the probe attempt (success or failure).
	if m.onProbeEvent != nil {
		m.onProbeEvent("latency")
	}

	if err := m.performLatencyProbe(hash, testURL); err != nil {
		log.Printf("[probe] latency probe failed for %s: %v", hash.Hex(), err)
		return
	}
}

func (m *ProbeManager) performEgressProbe(hash node.Hash) (netip.Addr, egressProbeErrorStage, error) {
	body, latency, err := m.fetcher(hash, egressTraceURL)
	if err != nil {
		m.pool.RecordResult(hash, false)
		m.pool.UpdateNodeEgressIP(hash, nil, nil)
		return netip.Addr{}, egressProbeFetchError, err
	}

	m.pool.RecordResult(hash, true)
	if latency > 0 {
		m.pool.RecordLatency(hash, egressTraceDomain, &latency)
	}

	ip, loc, err := ParseCloudflareTrace(body)
	if err != nil {
		m.pool.UpdateNodeEgressIP(hash, nil, nil)
		return netip.Addr{}, egressProbeParseError, err
	}
	m.pool.UpdateNodeEgressIP(hash, &ip, loc)
	return ip, egressProbeNoError, nil
}

func (m *ProbeManager) performLatencyProbe(hash node.Hash, testURL string) error {
	domain := netutil.ExtractDomain(testURL)
	_, latency, err := m.fetcher(hash, testURL)
	if err != nil {
		m.pool.RecordResult(hash, false)
		m.pool.RecordLatency(hash, domain, nil)
		return err
	}

	m.pool.RecordResult(hash, true)
	m.pool.RecordLatency(hash, domain, &latency)
	return nil
}

func (m *ProbeManager) currentLatencyTestURL() string {
	testURL := defaultLatencyTestURL
	if m.latencyTestURL != nil {
		testURL = m.latencyTestURL()
	}
	return testURL
}
