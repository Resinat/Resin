// Package topology coordinates the subscription → node pool → platform view pipeline.
// It owns the GlobalNodePool, PlatformManager, and SubscriptionManager,
// breaking import cycles between the leaf packages (node, subscription, platform).
package topology

import (
	"encoding/json"
	"net/netip"
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
	platMu    sync.RWMutex
	platforms []*platform.Platform

	// Subscription lookup — injected by SubscriptionManager.
	subLookup func(subID string) *subscription.Subscription

	// GeoIP lookup — injected at construction.
	geoLookup platform.GeoLookupFunc

	// Persistence callbacks (optional, nil in tests without persistence).
	onNodeAdded      func(hash node.Hash) // called after a new node is created
	onNodeRemoved    func(hash node.Hash) // called after a node is deleted from pool
	onSubNodeChanged func(subID string, hash node.Hash, added bool)

	// Health callbacks (optional).
	onNodeDynamicChanged func(hash node.Hash)                // fired on circuit/failure/egress changes
	onNodeLatencyChanged func(hash node.Hash, domain string) // fired on latency record updates

	// Health config
	maxLatencyTableEntries int
	maxConsecutiveFailures int
	latencyDecayWindow     func() time.Duration
}

// PoolConfig configures the GlobalNodePool.
type PoolConfig struct {
	SubLookup              func(subID string) *subscription.Subscription
	GeoLookup              platform.GeoLookupFunc
	OnNodeAdded            func(hash node.Hash)
	OnNodeRemoved          func(hash node.Hash)
	OnSubNodeChanged       func(subID string, hash node.Hash, added bool)
	OnNodeDynamicChanged   func(hash node.Hash)
	OnNodeLatencyChanged   func(hash node.Hash, domain string)
	MaxLatencyTableEntries int
	MaxConsecutiveFailures int
	LatencyDecayWindow     func() time.Duration
}

// NewGlobalNodePool creates a new GlobalNodePool.
func NewGlobalNodePool(cfg PoolConfig) *GlobalNodePool {
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
		maxConsecutiveFailures: cfg.MaxConsecutiveFailures,
		latencyDecayWindow:     cfg.LatencyDecayWindow,
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
	p.nodes.Compute(hash, func(entry *node.NodeEntry, loaded bool) (*node.NodeEntry, xsync.ComputeOp) {
		if !loaded {
			return entry, xsync.CancelOp // idempotent no-op
		}
		empty := entry.RemoveSubscriptionID(subID)
		if empty {
			wasDeleted = true
			return nil, xsync.DeleteOp
		}
		return entry, xsync.UpdateOp
	})

	if p.onSubNodeChanged != nil {
		p.onSubNodeChanged(subID, hash, false)
	}
	if wasDeleted && p.onNodeRemoved != nil {
		p.onNodeRemoved(hash)
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
	p.platforms = append(p.platforms, plat)
}

// UnregisterPlatform removes a platform from dirty notifications.
func (p *GlobalNodePool) UnregisterPlatform(id string) {
	p.platMu.Lock()
	defer p.platMu.Unlock()
	for i, plat := range p.platforms {
		if plat.ID == id {
			p.platforms = append(p.platforms[:i], p.platforms[i+1:]...)
			return
		}
	}
}

// makeSubLookup builds the SubLookupFunc closure for MatchRegexs.
func (p *GlobalNodePool) makeSubLookup() node.SubLookupFunc {
	return func(subID string, hash node.Hash) (string, bool, []string, bool) {
		sub := p.subLookup(subID)
		if sub == nil {
			return "", false, nil, false
		}
		tags, _ := sub.ManagedNodes().Load(hash)
		return sub.Name(), sub.Enabled(), tags, true
	}
}

// notifyAllPlatformsDirty tells every registered platform to re-evaluate a node.
func (p *GlobalNodePool) notifyAllPlatformsDirty(hash node.Hash) {
	p.platMu.RLock()
	defer p.platMu.RUnlock()

	subLookup := p.makeSubLookup()
	getEntry := func(h node.Hash) (*node.NodeEntry, bool) {
		return p.nodes.Load(h)
	}

	for _, plat := range p.platforms {
		plat.NotifyDirty(hash, getEntry, subLookup, p.geoLookup)
	}
}

// RebuildAllPlatforms triggers a full rebuild on all registered platforms.
func (p *GlobalNodePool) RebuildAllPlatforms() {
	p.platMu.RLock()
	defer p.platMu.RUnlock()

	subLookup := p.makeSubLookup()
	poolRange := func(fn func(node.Hash, *node.NodeEntry) bool) {
		p.nodes.Range(fn)
	}

	for _, plat := range p.platforms {
		plat.FullRebuild(poolRange, subLookup, p.geoLookup)
	}
}

// RebuildPlatform triggers a full rebuild on a specific platform.
func (p *GlobalNodePool) RebuildPlatform(plat *platform.Platform) {
	subLookup := p.makeSubLookup()
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
		if p.maxConsecutiveFailures > 0 && int(newCount) >= p.maxConsecutiveFailures {
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

// RecordLatency records a latency observation for the given node and raw target.
// rawTarget is passed through ExtractDomain internally for eTLD+1 normalization.
func (p *GlobalNodePool) RecordLatency(hash node.Hash, rawTarget string, latency time.Duration) {
	entry, ok := p.nodes.Load(hash)
	if !ok || entry.LatencyTable == nil {
		return
	}

	domain := netutil.ExtractDomain(rawTarget)
	var decayWindow time.Duration
	if p.latencyDecayWindow != nil {
		decayWindow = p.latencyDecayWindow()
	}
	if decayWindow <= 0 {
		decayWindow = 30 * time.Second // default
	}

	wasEmpty := entry.LatencyTable.Update(domain, latency, decayWindow)

	// If the table transitioned from empty to non-empty, the node might
	// now satisfy the HasLatency filter — notify platforms.
	if wasEmpty {
		p.notifyAllPlatformsDirty(hash)
	}

	if p.onNodeLatencyChanged != nil {
		p.onNodeLatencyChanged(hash, domain)
	}
}

// UpdateNodeEgressIP updates the node's egress IP if it changed.
// Always updates LastEgressUpdate to record a successful egress-IP sample time.
// Fires OnNodeDynamicChanged and notifies platforms only on actual IP change.
func (p *GlobalNodePool) UpdateNodeEgressIP(hash node.Hash, ip netip.Addr) {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return
	}

	// Record successful egress-IP sample timestamp.
	entry.LastEgressUpdate.Store(time.Now().UnixNano())

	old := entry.GetEgressIP()
	if old == ip {
		return // no IP change — skip notifications
	}

	entry.SetEgressIP(ip)

	p.notifyAllPlatformsDirty(hash)
	if p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}
}
