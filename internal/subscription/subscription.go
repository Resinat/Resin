// Package subscription provides subscription types and parsing logic.
package subscription

import (
	"sync"
	"sync/atomic"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/resin-proxy/resin/internal/node"
)

// Subscription represents a subscription's runtime state.
// The per-subscription operation lock (serializing update/rename/eviction)
// lives in topology.SubscriptionManager, not here.
type Subscription struct {
	// Immutable after creation.
	ID               string
	URL              string
	UpdateIntervalNs int64 // configured interval, set at creation

	// Mutable fields guarded by mu.
	mu        sync.RWMutex
	name      string
	enabled   bool
	ephemeral bool

	// Persistence timestamps (written under mu or single-writer context).
	CreatedAtNs int64
	UpdatedAtNs int64

	// Runtime-only fields (NOT persisted). Atomic for lock-free reads
	// from the scheduler's due-check loop.
	LastCheckedNs atomic.Int64
	LastUpdatedNs atomic.Int64
	LastError     atomic.Pointer[string]

	// managedNodes is the subscription's node view: Hash → Tags.
	// Swapped atomically on subscription update.
	managedNodes atomic.Pointer[xsync.Map[node.Hash, []string]]
}

// NewSubscription creates a Subscription with an empty ManagedNodes map.
func NewSubscription(id, name, url string, enabled, ephemeral bool) *Subscription {
	s := &Subscription{
		ID:        id,
		URL:       url,
		name:      name,
		enabled:   enabled,
		ephemeral: ephemeral,
	}
	empty := xsync.NewMap[node.Hash, []string]()
	s.managedNodes.Store(empty)
	emptyErr := ""
	s.LastError.Store(&emptyErr)
	return s
}

// SetLastError atomically sets the last error string.
func (s *Subscription) SetLastError(err string) { s.LastError.Store(&err) }

// GetLastError atomically loads the last error string.
func (s *Subscription) GetLastError() string { return *s.LastError.Load() }

// Name returns the subscription name (thread-safe).
func (s *Subscription) Name() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.name
}

// SetName updates the subscription name (thread-safe).
func (s *Subscription) SetName(name string) {
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
}

// Enabled returns whether the subscription is enabled (thread-safe).
func (s *Subscription) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// SetEnabled updates the enabled flag (thread-safe).
func (s *Subscription) SetEnabled(v bool) {
	s.mu.Lock()
	s.enabled = v
	s.mu.Unlock()
}

// Ephemeral returns whether the subscription is ephemeral (thread-safe).
func (s *Subscription) Ephemeral() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ephemeral
}

// ManagedNodes returns the current node view via atomic load.
func (s *Subscription) ManagedNodes() *xsync.Map[node.Hash, []string] {
	return s.managedNodes.Load()
}

// SwapManagedNodes atomically replaces the managed nodes view.
func (s *Subscription) SwapManagedNodes(m *xsync.Map[node.Hash, []string]) {
	s.managedNodes.Store(m)
}

// DiffHashes computes the hash diff between old and new managed-nodes maps.
// Returns slices of added, kept, and removed hashes.
func DiffHashes(
	oldMap, newMap *xsync.Map[node.Hash, []string],
) (added, kept, removed []node.Hash) {
	// Hashes only in new → added. Hashes in both → kept.
	newMap.Range(func(h node.Hash, _ []string) bool {
		if _, ok := oldMap.Load(h); ok {
			kept = append(kept, h)
		} else {
			added = append(added, h)
		}
		return true
	})

	// Hashes only in old → removed.
	oldMap.Range(func(h node.Hash, _ []string) bool {
		if _, ok := newMap.Load(h); !ok {
			removed = append(removed, h)
		}
		return true
	})

	return added, kept, removed
}
