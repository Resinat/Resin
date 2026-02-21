// Package topology coordinates the subscription → node pool → platform view pipeline.
// It owns the GlobalNodePool, PlatformManager, and SubscriptionManager,
// breaking import cycles between the leaf packages (node, subscription, platform).
package topology

import (
	"encoding/json"
	"errors"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/resin-proxy/resin/internal/netutil"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/subscription"
)

// GlobalNodePool is the system's single source of truth for nodes.
// It uses xsync.Map for concurrent access and xsync.Compute for atomic
// AddNodeFromSub / RemoveNodeFromSub operations.
type GlobalNodePool struct {
	nodes *xsync.Map[node.Hash, *node.NodeEntry]

	// Platform references for dirty-notify.
	platMu         sync.RWMutex
	platformByID   map[string]*platform.Platform // id -> platform
	platformByName map[string]*platform.Platform // name -> platform

	// Subscription lookup — injected by SubscriptionManager.
	subLookup func(subID string) *subscription.Subscription

	// GeoIP lookup — injected at construction.
	geoLookup platform.GeoLookupFunc

	// Persistence callbacks (optional, nil in tests without persistence).
	onNodeAdded      func(hash node.Hash)                        // called after a new node is created
	onNodeRemoved    func(hash node.Hash, entry *node.NodeEntry) // called after a node is deleted from pool
	onSubNodeChanged func(subID string, hash node.Hash, added bool)

	// Health callbacks (optional).
	onNodeDynamicChanged func(hash node.Hash)                // fired on circuit/failure/egress changes
	onNodeLatencyChanged func(hash node.Hash, domain string) // fired on latency record updates

	// Health config
	maxLatencyTableEntries int
	maxConsecutiveFailures func() int
	latencyDecayWindow     func() time.Duration
	latencyAuthorities     func() []string
}

// PoolConfig configures the GlobalNodePool.
type PoolConfig struct {
	SubLookup              func(subID string) *subscription.Subscription
	GeoLookup              platform.GeoLookupFunc
	OnNodeAdded            func(hash node.Hash)
	OnNodeRemoved          func(hash node.Hash, entry *node.NodeEntry)
	OnSubNodeChanged       func(subID string, hash node.Hash, added bool)
	OnNodeDynamicChanged   func(hash node.Hash)
	OnNodeLatencyChanged   func(hash node.Hash, domain string)
	MaxLatencyTableEntries int
	MaxConsecutiveFailures func() int
	LatencyDecayWindow     func() time.Duration
	LatencyAuthorities     func() []string
}

var (
	// ErrPlatformNotRegistered indicates the target platform ID is not registered.
	ErrPlatformNotRegistered = errors.New("platform not registered")
	// ErrPlatformNameConflict indicates another platform already uses the target name.
	ErrPlatformNameConflict = errors.New("platform name conflict")
)

// NewGlobalNodePool creates a new GlobalNodePool.
func NewGlobalNodePool(cfg PoolConfig) *GlobalNodePool {
	maxConsecutiveFailuresFn := cfg.MaxConsecutiveFailures
	if maxConsecutiveFailuresFn == nil {
		panic("topology: NewGlobalNodePool requires non-nil MaxConsecutiveFailures")
	}

	return &GlobalNodePool{
		nodes:                  xsync.NewMap[node.Hash, *node.NodeEntry](),
		subLookup:              cfg.SubLookup,
		geoLookup:              cfg.GeoLookup,
		onNodeAdded:            cfg.OnNodeAdded,
		onNodeRemoved:          cfg.OnNodeRemoved,
		onSubNodeChanged:       cfg.OnSubNodeChanged,
		onNodeDynamicChanged:   cfg.OnNodeDynamicChanged,
		onNodeLatencyChanged:   cfg.OnNodeLatencyChanged,
		maxLatencyTableEntries: cfg.MaxLatencyTableEntries,
		maxConsecutiveFailures: maxConsecutiveFailuresFn,
		latencyDecayWindow:     cfg.LatencyDecayWindow,
		latencyAuthorities:     cfg.LatencyAuthorities,
		platformByID:           make(map[string]*platform.Platform),
		platformByName:         make(map[string]*platform.Platform),
	}
}

// AddNodeFromSub adds a node to the pool with the given subscription reference.
// Uses xsync.Compute for atomic load-or-create + ref-update.
// Idempotent: adding the same (hash, subID) pair multiple times is safe.
// After mutation, notifies all platforms to re-evaluate the node.
func (p *GlobalNodePool) AddNodeFromSub(hash node.Hash, rawOpts json.RawMessage, subID string) {
	isNew := false
	p.nodes.Compute(hash, func(entry *node.NodeEntry, loaded bool) (*node.NodeEntry, xsync.ComputeOp) {
		if !loaded {
			entry = node.NewNodeEntry(hash, rawOpts, time.Now(), p.maxLatencyTableEntries)
			isNew = true
		}
		entry.AddSubscriptionID(subID)
		return entry, xsync.UpdateOp
	})

	if isNew && p.onNodeAdded != nil {
		p.onNodeAdded(hash)
	}
	if p.onSubNodeChanged != nil {
		p.onSubNodeChanged(subID, hash, true)
	}

	p.notifyAllPlatformsDirty(hash)
}

// RemoveNodeFromSub removes a subscription reference from a node.
// If the node has no remaining references, it is deleted from the pool.
// Uses xsync.Compute for atomic ref-update + conditional delete.
// Idempotent: removing a nonexistent (hash, subID) pair is safe.
func (p *GlobalNodePool) RemoveNodeFromSub(hash node.Hash, subID string) {
	wasDeleted := false
	var deletedEntry *node.NodeEntry // capture entry before map deletion
	p.nodes.Compute(hash, func(entry *node.NodeEntry, loaded bool) (*node.NodeEntry, xsync.ComputeOp) {
		if !loaded {
			return entry, xsync.CancelOp // idempotent no-op
		}
		empty := entry.RemoveSubscriptionID(subID)
		if empty {
			wasDeleted = true
			deletedEntry = entry
			return nil, xsync.DeleteOp
		}
		return entry, xsync.UpdateOp
	})

	if p.onSubNodeChanged != nil {
		p.onSubNodeChanged(subID, hash, false)
	}
	if wasDeleted && p.onNodeRemoved != nil {
		p.onNodeRemoved(hash, deletedEntry)
	}

	p.notifyAllPlatformsDirty(hash)
}

// GetEntry retrieves a node entry by hash.
func (p *GlobalNodePool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	return p.nodes.Load(hash)
}

// Range iterates all nodes in the pool.
func (p *GlobalNodePool) Range(fn func(node.Hash, *node.NodeEntry) bool) {
	p.nodes.Range(fn)
}

// Size returns the number of nodes in the pool.
func (p *GlobalNodePool) Size() int {
	return p.nodes.Size()
}

// LoadNodeFromBootstrap inserts a node during bootstrap recovery.
// No dirty-marks, no platform notifications.
func (p *GlobalNodePool) LoadNodeFromBootstrap(entry *node.NodeEntry) {
	p.nodes.Store(entry.Hash, entry)
}

// RegisterPlatform adds a platform to receive dirty notifications.
func (p *GlobalNodePool) RegisterPlatform(plat *platform.Platform) {
	p.platMu.Lock()
	defer p.platMu.Unlock()
	// Check for existing ID to avoid duplicates.
	if _, exists := p.platformByID[plat.ID]; exists {
		return
	}
	p.platformByID[plat.ID] = plat
	if plat.Name != "" {
		p.platformByName[plat.Name] = plat
	}
}

// UnregisterPlatform removes a platform from dirty notifications.
func (p *GlobalNodePool) UnregisterPlatform(id string) {
	p.platMu.Lock()
	defer p.platMu.Unlock()
	plat, ok := p.platformByID[id]
	if !ok {
		return
	}
	delete(p.platformByID, id)
	if plat.Name != "" {
		// Only delete when name still points at this platform.
		if current, ok := p.platformByName[plat.Name]; ok && current == plat {
			delete(p.platformByName, plat.Name)
		}
	}
}

// ReplacePlatform atomically replaces an existing platform object by ID.
// It follows a copy-on-write update path: the caller builds a new Platform
// instance, this method rebuilds its routable view, then swaps map pointers
// under platMu in one critical section.
func (p *GlobalNodePool) ReplacePlatform(next *platform.Platform) error {
	if next == nil || next.ID == "" {
		return ErrPlatformNotRegistered
	}

	// Build the new platform's view before publish so readers never observe
	// an empty, not-yet-built view due only to replacement.
	p.RebuildPlatform(next)

	p.platMu.Lock()
	defer p.platMu.Unlock()

	current, ok := p.platformByID[next.ID]
	if !ok {
		return ErrPlatformNotRegistered
	}

	if next.Name != "" {
		if existingByName, exists := p.platformByName[next.Name]; exists && existingByName != current {
			return ErrPlatformNameConflict
		}
	}

	p.platformByID[next.ID] = next

	if current.Name != "" {
		if mapped, exists := p.platformByName[current.Name]; exists && mapped == current {
			delete(p.platformByName, current.Name)
		}
	}
	if next.Name != "" {
		p.platformByName[next.Name] = next
	}

	return nil
}

// GetPlatform retrieves a platform by ID.
func (p *GlobalNodePool) GetPlatform(id string) (*platform.Platform, bool) {
	p.platMu.RLock()
	defer p.platMu.RUnlock()
	plat, ok := p.platformByID[id]
	return plat, ok
}

// GetPlatformByName retrieves a platform by Name.
func (p *GlobalNodePool) GetPlatformByName(name string) (*platform.Platform, bool) {
	p.platMu.RLock()
	defer p.platMu.RUnlock()
	plat, ok := p.platformByName[name]
	return plat, ok
}

// RangePlatforms iterates all registered platforms.
func (p *GlobalNodePool) RangePlatforms(fn func(*platform.Platform) bool) {
	for _, plat := range p.platformSnapshot() {
		if !fn(plat) {
			return
		}
	}
}

func (p *GlobalNodePool) platformSnapshot() []*platform.Platform {
	p.platMu.RLock()
	defer p.platMu.RUnlock()

	platforms := make([]*platform.Platform, 0, len(p.platformByID))
	for _, plat := range p.platformByID {
		platforms = append(platforms, plat)
	}
	return platforms
}

// MakeSubLookup builds the SubLookupFunc closure for MatchRegexs / tag resolution.
func (p *GlobalNodePool) MakeSubLookup() node.SubLookupFunc {
	return func(subID string, hash node.Hash) (string, bool, []string, bool) {
		sub := p.subLookup(subID)
		if sub == nil {
			return "", false, nil, false
		}
		tags, _ := sub.ManagedNodes().Load(hash)
		return sub.Name(), sub.Enabled(), tags, true
	}
}

// ResolveNodeDisplayTag resolves a node hash to its display tag for request logs.
// Rule (DESIGN.md §601):
//  1. Among subscriptions that hold this node, choose the earliest-created one.
//  2. Within that subscription, choose lexicographically smallest tag.
//  3. Return "<SubscriptionName>/<Tag>".
//
// Returns empty string when resolution is not possible.
func (p *GlobalNodePool) ResolveNodeDisplayTag(hash node.Hash) string {
	if p.subLookup == nil {
		return ""
	}

	entry, ok := p.GetEntry(hash)
	if !ok || entry == nil {
		return ""
	}
	subIDs := entry.SubscriptionIDs()
	if len(subIDs) == 0 {
		return ""
	}

	bestFound := false
	var bestCreatedAtNs int64
	var bestSubID string
	var bestSubName string
	var bestTag string

	for _, subID := range subIDs {
		sub := p.subLookup(subID)
		if sub == nil {
			continue
		}

		tags, ok := sub.ManagedNodes().Load(hash)
		if !ok || len(tags) == 0 {
			continue
		}

		smallestTag := tags[0]
		for _, tag := range tags[1:] {
			if tag < smallestTag {
				smallestTag = tag
			}
		}

		createdAtNs := sub.CreatedAtNs
		if !bestFound ||
			createdAtNs < bestCreatedAtNs ||
			(createdAtNs == bestCreatedAtNs && subID < bestSubID) {
			bestFound = true
			bestCreatedAtNs = createdAtNs
			bestSubID = subID
			bestSubName = sub.Name()
			bestTag = smallestTag
		}
	}

	if !bestFound || bestSubName == "" || bestTag == "" {
		return ""
	}
	return bestSubName + "/" + bestTag
}

// notifyAllPlatformsDirty tells every registered platform to re-evaluate a node.
func (p *GlobalNodePool) notifyAllPlatformsDirty(hash node.Hash) {
	platforms := p.platformSnapshot()
	if len(platforms) == 0 {
		return
	}

	subLookup := p.MakeSubLookup()
	getEntry := func(h node.Hash) (*node.NodeEntry, bool) {
		return p.nodes.Load(h)
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > len(platforms) {
		workers = len(platforms)
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, plat := range platforms {
		sem <- struct{}{}
		wg.Add(1)
		go func(plat *platform.Platform) {
			defer wg.Done()
			defer func() { <-sem }()
			plat.NotifyDirty(hash, getEntry, subLookup, p.geoLookup)
		}(plat)
	}
	wg.Wait()
}

// RebuildAllPlatforms triggers a full rebuild on all registered platforms.
func (p *GlobalNodePool) RebuildAllPlatforms() {
	platforms := p.platformSnapshot()
	subLookup := p.MakeSubLookup()
	poolRange := func(fn func(node.Hash, *node.NodeEntry) bool) {
		p.nodes.Range(fn)
	}

	for _, plat := range platforms {
		plat.FullRebuild(poolRange, subLookup, p.geoLookup)
	}
}

// RebuildPlatform triggers a full rebuild on a specific platform.
func (p *GlobalNodePool) RebuildPlatform(plat *platform.Platform) {
	subLookup := p.MakeSubLookup()
	poolRange := func(fn func(node.Hash, *node.NodeEntry) bool) {
		p.nodes.Range(fn)
	}
	plat.FullRebuild(poolRange, subLookup, p.geoLookup)
}

// --- Health Management ---

// SetOnNodeAdded sets the callback fired when a new node is added.
// Must be called before any background workers are started.
func (p *GlobalNodePool) SetOnNodeAdded(fn func(hash node.Hash)) {
	p.onNodeAdded = fn
}

// SetOnNodeRemoved sets the callback fired when a node is removed from the pool.
// Must be called before any background workers are started.
func (p *GlobalNodePool) SetOnNodeRemoved(fn func(hash node.Hash, entry *node.NodeEntry)) {
	p.onNodeRemoved = fn
}

// NotifyNodeDirty triggers platform re-evaluation for a single node.
// Used by OutboundManager after outbound creation to update routable views.
func (p *GlobalNodePool) NotifyNodeDirty(hash node.Hash) {
	p.notifyAllPlatformsDirty(hash)
}

// RangeNodes iterates over all nodes in the pool.
// The callback receives each node's hash and entry. Return false to stop.
func (p *GlobalNodePool) RangeNodes(fn func(node.Hash, *node.NodeEntry) bool) {
	p.nodes.Range(fn)
}

// RecordResult records a probe or passive health-check result.
// On success, resets FailureCount and clears circuit-breaker.
// On failure, increments FailureCount and opens circuit-breaker if threshold is reached.
// Notifies platforms only when circuit state changes (open/recover).
// Fires OnNodeDynamicChanged only when dynamic fields actually change.
func (p *GlobalNodePool) RecordResult(hash node.Hash, success bool) {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return
	}

	dynamicChanged := false
	circuitStateChanged := false

	if success {
		if entry.FailureCount.Swap(0) != 0 {
			dynamicChanged = true
		}
		if entry.CircuitOpenSince.Swap(0) != 0 {
			dynamicChanged = true
			circuitStateChanged = true
		}
	} else {
		newCount := entry.FailureCount.Add(1)
		dynamicChanged = true
		maxConsecutiveFailures := p.currentMaxConsecutiveFailures()
		if maxConsecutiveFailures > 0 && int(newCount) >= maxConsecutiveFailures {
			// Open circuit if not already open.
			if entry.CircuitOpenSince.CompareAndSwap(0, time.Now().UnixNano()) {
				circuitStateChanged = true
			}
		}
	}

	if circuitStateChanged {
		p.notifyAllPlatformsDirty(hash)
	}
	if dynamicChanged && p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}
}

func (p *GlobalNodePool) currentMaxConsecutiveFailures() int {
	return p.maxConsecutiveFailures()
}

// RecordLatency records a latency probe attempt for the given node and raw target.
// rawTarget is normalized through ExtractDomain (eTLD+1). latency may be nil,
// which means "attempt only" without latency sample writeback.
func (p *GlobalNodePool) RecordLatency(hash node.Hash, rawTarget string, latency *time.Duration) {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return
	}

	domain := netutil.ExtractDomain(rawTarget)
	nowNs := time.Now().UnixNano()
	entry.LastLatencyProbeAttempt.Store(nowNs)
	if p.isAuthorityDomain(domain) {
		entry.LastAuthorityLatencyProbeAttempt.Store(nowNs)
	}
	if p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}

	if latency == nil || *latency <= 0 || entry.LatencyTable == nil {
		return
	}

	var decayWindow time.Duration
	if p.latencyDecayWindow != nil {
		decayWindow = p.latencyDecayWindow()
	}
	if decayWindow <= 0 {
		decayWindow = 30 * time.Second // default
	}

	wasEmpty := entry.LatencyTable.Update(domain, *latency, decayWindow)

	// If the table transitioned from empty to non-empty, the node might
	// now satisfy the HasLatency filter — notify platforms.
	if wasEmpty {
		p.notifyAllPlatformsDirty(hash)
	}

	if p.onNodeLatencyChanged != nil {
		p.onNodeLatencyChanged(hash, domain)
	}
}

// UpdateNodeEgressIP records an egress probe attempt and optionally updates
// the node's egress IP when ip != nil.
func (p *GlobalNodePool) UpdateNodeEgressIP(hash node.Hash, ip *netip.Addr) {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return
	}

	nowNs := time.Now().UnixNano()
	entry.LastEgressUpdateAttempt.Store(nowNs)

	if ip == nil {
		if p.onNodeDynamicChanged != nil {
			p.onNodeDynamicChanged(hash)
		}
		return
	}

	// Record successful egress-IP sample timestamp.
	entry.LastEgressUpdate.Store(nowNs)
	old := entry.GetEgressIP()
	if old == *ip {
		if p.onNodeDynamicChanged != nil {
			p.onNodeDynamicChanged(hash)
		}
		return // no IP change — skip platform notifications
	}

	entry.SetEgressIP(*ip)

	p.notifyAllPlatformsDirty(hash)
	if p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}
}

func (p *GlobalNodePool) isAuthorityDomain(domain string) bool {
	if domain == "" || p.latencyAuthorities == nil {
		return false
	}
	authorities := p.latencyAuthorities()
	for _, authority := range authorities {
		if strings.EqualFold(strings.TrimSpace(authority), domain) {
			return true
		}
	}
	return false
}
