package probe

import (
	"fmt"
	"log"
	"math/rand/v2"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/scanloop"
	"github.com/Resinat/Resin/internal/topology"
	"github.com/puzpuzpuz/xsync/v4"
)

// Fetcher executes an HTTP request through the given node, returning
// response body and TLS handshake latency. This is injectable for testing.
type Fetcher func(hash node.Hash, url string) (body []byte, latency time.Duration, err error)

// ProbeConfig configures the ProbeManager.
// Field names align 1:1 with RuntimeConfig to prevent mis-wiring.
type ProbeConfig struct {
	Pool        *topology.GlobalNodePool
	Concurrency int // number of async probe workers
	// QueueCapacity is the per-priority async queue capacity.
	// If <= 0, defaults to max(1024, Concurrency*4).
	QueueCapacity int

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

	// ChooseNormalWhenBoth chooses whether to pop normal-priority queue when
	// both high and normal queues are non-empty.
	// Nil defaults to 10% chance.
	ChooseNormalWhenBoth func() bool
}

// ProbeManager schedules and executes active probes against nodes in the pool.
// It holds a direct reference to *topology.GlobalNodePool (no interface).
type ProbeManager struct {
	pool        *topology.GlobalNodePool
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
	fetcher     Fetcher
	workerCount int
	taskQueue   *probeTaskQueue
	taskStates  *xsync.Map[probeTaskKey, *probeTaskState]

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
	defaultQueueCap       = 1024
)

type probePriority uint8

const (
	probePriorityNormal probePriority = iota
	probePriorityHigh
)

type probeTaskKind uint8

const (
	probeTaskKindEgress probeTaskKind = iota
	probeTaskKindLatency
)

type probeTaskKey struct {
	hash node.Hash
	kind probeTaskKind
}

type probeTask struct {
	key probeTaskKey
}

type probeTaskState struct {
	flags atomic.Uint32
}

const (
	taskFlagQueued uint32 = 1 << iota
	taskFlagRunning
	taskFlagDirty
	taskFlagDirtyHigh
	taskFlagQueuedHigh
)

type probeTaskBuffer struct {
	items []probeTask
	head  int
}

func (b *probeTaskBuffer) len() int {
	return len(b.items) - b.head
}

func (b *probeTaskBuffer) push(task probeTask) {
	b.items = append(b.items, task)
}

func (b *probeTaskBuffer) pop() probeTask {
	task := b.items[b.head]
	b.head++
	if b.head >= len(b.items) {
		b.items = nil
		b.head = 0
		return task
	}
	if b.head > 64 && b.head*2 >= len(b.items) {
		b.items = append([]probeTask(nil), b.items[b.head:]...)
		b.head = 0
	}
	return task
}

func (b *probeTaskBuffer) clear() {
	b.items = nil
	b.head = 0
}

type probeTaskQueue struct {
	mu                   sync.Mutex
	notEmpty             *sync.Cond
	high                 probeTaskBuffer
	normal               probeTaskBuffer
	highCap              int
	normalCap            int
	stopped              bool
	chooseNormalWhenBoth func() bool
}

func newProbeTaskQueue(highCap, normalCap int, chooseNormalWhenBoth func() bool) *probeTaskQueue {
	if highCap <= 0 {
		highCap = defaultQueueCap
	}
	if normalCap <= 0 {
		normalCap = defaultQueueCap
	}
	if chooseNormalWhenBoth == nil {
		chooseNormalWhenBoth = func() bool { return rand.IntN(10) == 0 }
	}
	q := &probeTaskQueue{
		highCap:              highCap,
		normalCap:            normalCap,
		chooseNormalWhenBoth: chooseNormalWhenBoth,
	}
	q.notEmpty = sync.NewCond(&q.mu)
	return q
}

func (q *probeTaskQueue) Enqueue(task probeTask, priority probePriority) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return false
	}

	switch priority {
	case probePriorityHigh:
		if q.high.len() >= q.highCap {
			return false
		}
		q.high.push(task)
	default:
		if q.normal.len() >= q.normalCap {
			return false
		}
		q.normal.push(task)
	}

	q.notEmpty.Signal()
	return true
}

func (q *probeTaskQueue) Dequeue() (probeTask, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		if q.stopped {
			return probeTask{}, false
		}

		highLen := q.high.len()
		normalLen := q.normal.len()
		switch {
		case highLen > 0 && normalLen > 0:
			if q.chooseNormalWhenBoth() {
				return q.normal.pop(), true
			}
			return q.high.pop(), true
		case highLen > 0:
			return q.high.pop(), true
		case normalLen > 0:
			return q.normal.pop(), true
		default:
			q.notEmpty.Wait()
		}
	}
}

func (q *probeTaskQueue) StopDropPending() {
	q.mu.Lock()
	q.stopped = true
	q.high.clear()
	q.normal.clear()
	q.mu.Unlock()
	q.notEmpty.Broadcast()
}

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
	queueCap := cfg.QueueCapacity
	if queueCap <= 0 {
		queueCap = conc * 4
		if queueCap < defaultQueueCap {
			queueCap = defaultQueueCap
		}
	}

	return &ProbeManager{
		pool:                            cfg.Pool,
		stopCh:                          make(chan struct{}),
		fetcher:                         cfg.Fetcher,
		workerCount:                     conc,
		taskQueue:                       newProbeTaskQueue(queueCap, queueCap, cfg.ChooseNormalWhenBoth),
		taskStates:                      xsync.NewMap[probeTaskKey, *probeTaskState](),
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

	for i := 0; i < m.workerCount; i++ {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runProbeWorker()
		}()
	}
}

// Stop signals all probe workers to stop and waits for completion.
//
// Design note:
//   - In-flight worker tasks are drained before Stop returns.
//   - Pending queued tasks are dropped on stop.
//   - We intentionally do not reject post-stop triggers via extra manager-global
//     state; expected ownership is that callers stop upstream event sources first.
func (m *ProbeManager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
		m.taskQueue.StopDropPending()
	})
	m.wg.Wait()
}

// TriggerImmediateEgressProbe enqueues an async egress probe for a node.
// Caller returns immediately.
func (m *ProbeManager) TriggerImmediateEgressProbe(hash node.Hash) {
	m.enqueueProbe(hash, probeTaskKindEgress, probePriorityNormal)
}

// TriggerImmediateLatencyProbe enqueues an async latency probe for a node.
// Caller returns immediately.
func (m *ProbeManager) TriggerImmediateLatencyProbe(hash node.Hash) {
	m.enqueueProbe(hash, probeTaskKindLatency, probePriorityNormal)
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
	select {
	case <-m.stopCh:
		return nil, fmt.Errorf("probe manager stopped")
	default:
	}

	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return nil, fmt.Errorf("node not found")
	}
	if entry.Outbound.Load() == nil {
		return nil, fmt.Errorf("node outbound not ready")
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
	select {
	case <-m.stopCh:
		return nil, fmt.Errorf("probe manager stopped")
	default:
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
	subLookup := m.pool.MakeSubLookup()

	m.pool.Range(func(h node.Hash, entry *node.NodeEntry) bool {
		// Check stop signal.
		select {
		case <-m.stopCh:
			return false
		default:
		}

		if entry.IsDisabledBySubscriptions(subLookup) {
			return true // disabled node -> skip periodic probe
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

		m.enqueueProbe(h, probeTaskKindEgress, probePriorityNormal)

		return true
	})
}

// scanLatency iterates all pool nodes and probes those due for latency check.
func (m *ProbeManager) scanLatency() {
	now := time.Now()
	maxLatencyInterval := 1 * time.Hour // default
	if m.maxLatencyTestInterval != nil {
		maxLatencyInterval = m.maxLatencyTestInterval()
	}
	maxAuthorityInterval := 3 * time.Hour // default
	if m.maxAuthorityLatencyTestInterval != nil {
		maxAuthorityInterval = m.maxAuthorityLatencyTestInterval()
	}
	lookahead := 15 * time.Second
	subLookup := m.pool.MakeSubLookup()
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

		if entry.IsDisabledBySubscriptions(subLookup) {
			return true // disabled node -> skip periodic probe
		}

		if entry.Outbound.Load() == nil {
			return true // skip nil outbound
		}

		if !m.isLatencyProbeDue(entry, now, maxLatencyInterval, maxAuthorityInterval, authorities, lookahead) {
			return true
		}

		m.enqueueProbe(h, probeTaskKindLatency, probePriorityNormal)

		return true
	})
}

func (m *ProbeManager) runProbeWorker() {
	for {
		task, ok := m.taskQueue.Dequeue()
		if !ok {
			return
		}

		state, ok := m.markTaskRunning(task.key)
		if !ok {
			continue
		}

		m.executeTask(task)
		m.finishTask(task.key, state)
	}
}

func (m *ProbeManager) executeTask(task probeTask) {
	entry, ok := m.pool.GetEntry(task.key.hash)
	if !ok || entry.Outbound.Load() == nil {
		return
	}
	if entry.IsDisabledBySubscriptions(m.pool.MakeSubLookup()) {
		return
	}

	switch task.key.kind {
	case probeTaskKindEgress:
		m.probeEgress(task.key.hash, entry)
	case probeTaskKindLatency:
		m.probeLatency(task.key.hash, entry, m.currentLatencyTestURL())
	}
}

func (m *ProbeManager) enqueueProbe(hash node.Hash, kind probeTaskKind, priority probePriority) bool {
	key := probeTaskKey{hash: hash, kind: kind}
	state, _ := m.taskStates.LoadOrCompute(key, func() (*probeTaskState, bool) {
		return &probeTaskState{}, false
	})
	allowQueuedHighUpgrade := priority == probePriorityHigh

	for {
		flags := state.flags.Load()
		if flags&taskFlagRunning != 0 {
			next := flags | taskFlagDirty
			if priority == probePriorityHigh {
				next |= taskFlagDirtyHigh
			}
			if state.flags.CompareAndSwap(flags, next) {
				return false
			}
			continue
		}

		if flags&taskFlagQueued != 0 {
			// If a normal-priority task is already queued, add a high-priority token
			// so the next dequeue can observe the upgraded urgency. The stale normal
			// token will later no-op when it reaches a worker.
			if allowQueuedHighUpgrade && flags&taskFlagQueuedHigh == 0 {
				next := flags | taskFlagQueuedHigh
				if !state.flags.CompareAndSwap(flags, next) {
					continue
				}
				if m.taskQueue.Enqueue(probeTask{key: key}, probePriorityHigh) {
					return true
				}
				for {
					current := state.flags.Load()
					revert := current &^ taskFlagQueuedHigh
					if state.flags.CompareAndSwap(current, revert) {
						break
					}
				}
				allowQueuedHighUpgrade = false
				continue
			}

			next := flags | taskFlagDirty
			if priority == probePriorityHigh {
				next |= taskFlagDirtyHigh
			}
			if state.flags.CompareAndSwap(flags, next) {
				return false
			}
			continue
		}

		next := flags | taskFlagQueued
		if priority == probePriorityHigh {
			next |= taskFlagQueuedHigh
		} else {
			next &^= taskFlagQueuedHigh
		}
		if !state.flags.CompareAndSwap(flags, next) {
			continue
		}

		if m.taskQueue.Enqueue(probeTask{key: key}, priority) {
			return true
		}

		m.clearDroppedState(state)
		m.tryDeleteTaskState(key, state)
		return false
	}
}

func (m *ProbeManager) markTaskRunning(key probeTaskKey) (*probeTaskState, bool) {
	state, ok := m.taskStates.Load(key)
	if !ok {
		return nil, false
	}

	for {
		flags := state.flags.Load()
		if flags&taskFlagQueued == 0 {
			m.tryDeleteTaskState(key, state)
			return nil, false
		}
		next := (flags | taskFlagRunning) &^ (taskFlagQueued | taskFlagQueuedHigh)
		if state.flags.CompareAndSwap(flags, next) {
			return state, true
		}
	}
}

func (m *ProbeManager) finishTask(key probeTaskKey, state *probeTaskState) {
	requeue := false
	requeuePriority := probePriorityNormal

	for {
		flags := state.flags.Load()
		next := flags &^ taskFlagRunning
		requeue = false

		if flags&taskFlagDirty != 0 {
			requeue = true
			next |= taskFlagQueued
			next &^= taskFlagDirty
			if flags&taskFlagDirtyHigh != 0 {
				next |= taskFlagQueuedHigh
				next &^= taskFlagDirtyHigh
				requeuePriority = probePriorityHigh
			} else {
				next &^= taskFlagQueuedHigh
				requeuePriority = probePriorityNormal
			}
		} else {
			next &^= (taskFlagDirty | taskFlagDirtyHigh | taskFlagQueuedHigh)
		}

		if state.flags.CompareAndSwap(flags, next) {
			break
		}
	}

	if requeue {
		if !m.taskQueue.Enqueue(probeTask{key: key}, requeuePriority) {
			m.clearDroppedState(state)
		}
	}

	m.tryDeleteTaskState(key, state)
}

func (m *ProbeManager) clearDroppedState(state *probeTaskState) {
	for {
		flags := state.flags.Load()
		next := flags &^ (taskFlagQueued | taskFlagQueuedHigh | taskFlagDirty | taskFlagDirtyHigh)
		if state.flags.CompareAndSwap(flags, next) {
			return
		}
	}
}

func (m *ProbeManager) tryDeleteTaskState(key probeTaskKey, state *probeTaskState) {
	m.taskStates.Compute(key, func(current *probeTaskState, loaded bool) (*probeTaskState, xsync.ComputeOp) {
		if !loaded || current != state {
			return current, xsync.CancelOp
		}
		if current.flags.Load() != 0 {
			return current, xsync.CancelOp
		}
		return nil, xsync.DeleteOp
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
		log.Printf("[probe] skip egress for %s: outbound not ready", hash.Hex())
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
		log.Printf("[probe] skip latency for %s: outbound not ready", hash.Hex())
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
