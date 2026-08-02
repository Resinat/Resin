package node

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

// SubLookupFunc resolves a subscription ID + node hash to the subscription's
// name, enabled status, and the tags for that node in that subscription.
// Returns ok=false if the subscription does not exist.
type SubLookupFunc func(subID string, hash Hash) (name string, enabled bool, tags []string, ok bool)

// TagFilter is a compiled set of line-oriented tag regex rules.
// Any rules are ORed, Must rules are ANDed, and any MustNot match rejects the node.
type TagFilter struct {
	Any     []*regexp.Regexp
	Must    []*regexp.Regexp
	MustNot []*regexp.Regexp
}

// Empty reports whether the filter contains no rules.
func (f TagFilter) Empty() bool {
	return len(f.Any) == 0 && len(f.Must) == 0 && len(f.MustNot) == 0
}

// NodeEntry represents a node in the global pool.
// Static fields are set at creation; dynamic fields use atomics or mutex.
type NodeEntry struct {
	// --- Static (immutable after creation) ---
	Hash       Hash
	RawOptions json.RawMessage
	CreatedAt  time.Time

	// --- Dynamic (guarded by mu) ---
	mu              sync.RWMutex
	subscriptionIDs []string
	LastError       string

	// Atomic dynamic fields for concurrent hot-path reads.
	FailureCount     atomic.Int32
	CircuitOpenSince atomic.Int64               // unix-nano; 0 = not open
	egressIP         atomic.Pointer[netip.Addr] // nil before first store
	egressRegion     atomic.Pointer[string]     // lowercase country code from probe trace; nil when unknown
	LastEgressUpdate atomic.Int64               // unix-nano of last successful egress-IP sample
	// Probe-attempt timestamps (unix-nano). These are updated regardless of
	// probe success/failure, and are used by probe schedulers.
	LastLatencyProbeAttempt          atomic.Int64
	LastAuthorityLatencyProbeAttempt atomic.Int64
	LastEgressUpdateAttempt          atomic.Int64
	LatencyTable                     *LatencyTable // per-domain latency stats; nil if not initialized

	// Admin-controlled disable flag. When true, the node is excluded from all
	// platform views regardless of subscription state. Toggled via the API.
	ManuallyDisabled atomic.Bool

	// Outbound instance for this node.
	Outbound atomic.Pointer[adapter.Outbound]
}

// NewNodeEntry creates a NodeEntry with the given static fields.
// maxLatencyTableEntries controls the bounded size of the regular-domain LRU
// partition in the per-domain latency table.
// Pass 0 to skip latency table initialization (e.g. in tests that don't need it).
func NewNodeEntry(hash Hash, rawOptions json.RawMessage, createdAt time.Time, maxLatencyTableEntries int) *NodeEntry {
	e := &NodeEntry{
		Hash:       hash,
		RawOptions: rawOptions,
		CreatedAt:  createdAt,
	}
	if maxLatencyTableEntries > 0 {
		e.LatencyTable = NewLatencyTable(maxLatencyTableEntries)
	}
	return e
}

// SubscriptionIDs returns a copy of the subscription ID slice (thread-safe).
func (e *NodeEntry) SubscriptionIDs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	cp := make([]string, len(e.subscriptionIDs))
	copy(cp, e.subscriptionIDs)
	return cp
}

// AddSubscriptionID adds subID to the subscription set if not already present.
// Must be called under external synchronization (e.g. xsync.Compute).
func (e *NodeEntry) AddSubscriptionID(subID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range e.subscriptionIDs {
		if id == subID {
			return // idempotent
		}
	}
	e.subscriptionIDs = append(e.subscriptionIDs, subID)
}

// RemoveSubscriptionID removes subID from the subscription set.
// Returns true if the set is now empty (node should be deleted).
// Must be called under external synchronization (e.g. xsync.Compute).
func (e *NodeEntry) RemoveSubscriptionID(subID string) (empty bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, id := range e.subscriptionIDs {
		if id == subID {
			e.subscriptionIDs = append(e.subscriptionIDs[:i], e.subscriptionIDs[i+1:]...)
			break
		}
	}
	return len(e.subscriptionIDs) == 0
}

// SubscriptionCount returns the number of subscriptions referencing this node.
func (e *NodeEntry) SubscriptionCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.subscriptionIDs)
}

// MatchRegexs tests whether the node matches ALL given regex filters.
// A match means any tag from any enabled subscription satisfies all regexes.
// Tags are tested in the format "<subscriptionName>/<tag>".
// For an empty regex list:
//   - if subLookup is nil, it matches everything (compatibility fallback);
//   - otherwise, it matches only when at least one enabled subscription exists.
func (e *NodeEntry) MatchRegexs(regexes []*regexp.Regexp, subLookup SubLookupFunc) bool {
	return e.MatchTagFilter(TagFilter{Must: regexes}, subLookup)
}

// MatchTagFilter tests the node's enabled-subscription tags against a compiled filter.
// Positive rules must be satisfied by one "<subscriptionName>/<tag>" candidate.
// MustNot rules inspect every candidate and reject the whole node on any match.
func (e *NodeEntry) MatchTagFilter(filter TagFilter, subLookup SubLookupFunc) bool {
	if subLookup == nil {
		return filter.Empty()
	}

	e.mu.RLock()
	subs := make([]string, len(e.subscriptionIDs))
	copy(subs, e.subscriptionIDs)
	e.mu.RUnlock()

	if len(subs) == 0 {
		return false
	}

	hasEnabledSubscription := false
	hasPositiveRules := len(filter.Any) > 0 || len(filter.Must) > 0
	matchedPositive := false

	for _, subID := range subs {
		name, enabled, tags, ok := subLookup(subID, e.Hash)
		if !ok || !enabled {
			continue
		}
		hasEnabledSubscription = true
		for _, tag := range tags {
			candidate := name + "/" + tag
			if matchesAny(candidate, filter.MustNot) {
				return false
			}
			if !matchedPositive &&
				matchesAll(candidate, filter.Must) &&
				(len(filter.Any) == 0 || matchesAny(candidate, filter.Any)) {
				matchedPositive = true
			}
		}
	}

	if !hasPositiveRules {
		return hasEnabledSubscription
	}
	return matchedPositive
}

// HasEnabledSubscription reports whether the node currently has at least one
// enabled subscription reference, based on subLookup.
//
// subLookup must apply the caller's definition of "subscription still holds
// this node" (for example, excluding evicted managed-node entries).
func (e *NodeEntry) HasEnabledSubscription(subLookup SubLookupFunc) bool {
	if e == nil || subLookup == nil {
		return false
	}

	e.mu.RLock()
	subs := make([]string, len(e.subscriptionIDs))
	copy(subs, e.subscriptionIDs)
	e.mu.RUnlock()

	for _, subID := range subs {
		_, enabled, _, ok := subLookup(subID, e.Hash)
		if ok && enabled {
			return true
		}
	}
	return false
}

// IsDisabledBySubscriptions reports whether the node should be treated as
// disabled: all referencing subscriptions are disabled (or missing/inapplicable
// by subLookup semantics).
func (e *NodeEntry) IsDisabledBySubscriptions(subLookup SubLookupFunc) bool {
	return !e.HasEnabledSubscription(subLookup)
}

// matchesAll returns true if s matches every regex in the list.
func matchesAll(s string, regexes []*regexp.Regexp) bool {
	for _, re := range regexes {
		if !re.MatchString(s) {
			return false
		}
	}
	return true
}

// matchesAny returns true if s matches at least one regex in the list.
func matchesAny(s string, regexes []*regexp.Regexp) bool {
	for _, re := range regexes {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// --- Condition helpers for platform filtering ---

// IsCircuitOpen returns true if the node is currently circuit-broken.
func (e *NodeEntry) IsCircuitOpen() bool {
	return e.CircuitOpenSince.Load() != 0
}

// IsManuallyDisabled reports whether an admin has flagged this node as disabled.
func (e *NodeEntry) IsManuallyDisabled() bool {
	if e == nil {
		return false
	}
	return e.ManuallyDisabled.Load()
}

// HasLatency returns true if the node has at least one latency record.
func (e *NodeEntry) HasLatency() bool {
	return e.LatencyTable != nil && e.LatencyTable.Size() > 0
}

// HasOutbound returns true if the node has a valid outbound instance.
func (e *NodeEntry) HasOutbound() bool {
	return e.Outbound.Load() != nil
}

// IsHealthy returns true when the node can be treated as healthy for
// routing/statistics: outbound is ready and circuit is not open.
func (e *NodeEntry) IsHealthy() bool {
	if e == nil {
		return false
	}
	return !e.IsCircuitOpen() && e.HasOutbound()
}

// GetEgressIP returns the node's egress IP, or the zero Addr if unknown.
func (e *NodeEntry) GetEgressIP() netip.Addr {
	ptr := e.egressIP.Load()
	if ptr == nil {
		return netip.Addr{}
	}
	return *ptr
}

// SetEgressIP stores the node's egress IP.
func (e *NodeEntry) SetEgressIP(ip netip.Addr) {
	e.egressIP.Store(&ip)
}

// GetEgressRegion returns the node's stored region from probe metadata,
// or empty string if unknown.
func (e *NodeEntry) GetEgressRegion() string {
	ptr := e.egressRegion.Load()
	if ptr == nil {
		return ""
	}
	return *ptr
}

// SetEgressRegion stores the node's explicit probe region.
// Empty input clears the stored value.
func (e *NodeEntry) SetEgressRegion(region string) {
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" {
		e.egressRegion.Store(nil)
		return
	}
	e.egressRegion.Store(&region)
}

// GetRegion resolves a node region using explicit probe metadata first,
// then GeoIP fallback from egress IP.
func (e *NodeEntry) GetRegion(geoLookup func(netip.Addr) string) string {
	if region := e.GetEgressRegion(); region != "" {
		return region
	}
	if geoLookup == nil {
		return ""
	}
	egressIP := e.GetEgressIP()
	if !egressIP.IsValid() {
		return ""
	}
	return geoLookup(egressIP)
}

// SetLastError sets the node's error string (thread-safe).
func (e *NodeEntry) SetLastError(msg string) {
	e.mu.Lock()
	e.LastError = msg
	e.mu.Unlock()
}

// GetLastError returns the node's error string (thread-safe).
func (e *NodeEntry) GetLastError() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.LastError
}
