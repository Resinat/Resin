package node

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

// SubLookupFunc resolves a subscription ID + node hash to the subscription's
// name, enabled status, and the tags for that node in that subscription.
// Returns ok=false if the subscription does not exist.
type SubLookupFunc func(subID string, hash Hash) (name string, enabled bool, tags []string, ok bool)

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
	LastEgressUpdate atomic.Int64               // unix-nano of last successful egress-IP sample
	LatencyTable     *LatencyTable              // per-domain latency stats; nil if not initialized

	// Outbound instance for this node.
	Outbound atomic.Pointer[adapter.Outbound]
}

// NewNodeEntry creates a NodeEntry with the given static fields.
// maxLatencyTableEntries controls the bounded size of the per-domain latency table.
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
// An empty regex list matches everything.
func (e *NodeEntry) MatchRegexs(regexes []*regexp.Regexp, subLookup SubLookupFunc) bool {
	if len(regexes) == 0 {
		return true
	}

	e.mu.RLock()
	subs := make([]string, len(e.subscriptionIDs))
	copy(subs, e.subscriptionIDs)
	e.mu.RUnlock()

	for _, subID := range subs {
		name, enabled, tags, ok := subLookup(subID, e.Hash)
		if !ok || !enabled {
			continue
		}
		for _, tag := range tags {
			candidate := name + "/" + tag
			if matchesAll(candidate, regexes) {
				return true
			}
		}
	}
	return false
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

// --- Condition helpers for platform filtering ---

// IsCircuitOpen returns true if the node is currently circuit-broken.
func (e *NodeEntry) IsCircuitOpen() bool {
	return e.CircuitOpenSince.Load() != 0
}

// HasLatency returns true if the node has at least one latency record.
func (e *NodeEntry) HasLatency() bool {
	return e.LatencyTable != nil && e.LatencyTable.Size() > 0
}

// HasOutbound returns true if the node has a valid outbound instance.
func (e *NodeEntry) HasOutbound() bool {
	return e.Outbound.Load() != nil
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
