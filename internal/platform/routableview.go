// Package platform provides Platform types and the sharded routable view.
package platform

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"

	"github.com/Resinat/Resin/internal/node"
)

const numShards = 64

// ReadOnlyView exposes only the read operations of RoutableView.
// This is the interface vended to external callers (data plane, API)
// so they cannot bypass FullRebuild/NotifyDirty to mutate the set.
type ReadOnlyView interface {
	Contains(h node.Hash) bool
	Size() int
	RandomPick(rng *rand.Rand) (node.Hash, bool)
	Range(fn func(node.Hash) bool)
}

// RoutableView is a 64-shard concurrent set supporting O(1) random pick,
// O(1) add, O(1) remove, and O(1) contains.
type RoutableView struct {
	shards [numShards]shard
	size   atomic.Int64 // total count across all shards
}

type shard struct {
	mu    sync.RWMutex
	nodes []node.Hash
	index map[node.Hash]int // hash → position in nodes slice
}

// NewRoutableView creates an empty RoutableView.
func NewRoutableView() *RoutableView {
	rv := &RoutableView{}
	for i := range rv.shards {
		rv.shards[i].index = make(map[node.Hash]int)
	}
	return rv
}

// shardFor returns the shard index for a given hash.
func shardFor(h node.Hash) int {
	// Use first byte for shard selection.
	return int(h[0]) % numShards
}

// Add inserts a hash into the view. No-op if already present.
func (rv *RoutableView) Add(h node.Hash) {
	s := &rv.shards[shardFor(h)]
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.index[h]; ok {
		return
	}
	s.index[h] = len(s.nodes)
	s.nodes = append(s.nodes, h)
	rv.size.Add(1)
}

// Remove deletes a hash from the view. No-op if absent.
// Uses swap-last-remove for O(1).
func (rv *RoutableView) Remove(h node.Hash) {
	s := &rv.shards[shardFor(h)]
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[h]
	if !ok {
		return
	}
	last := len(s.nodes) - 1
	if idx != last {
		s.nodes[idx] = s.nodes[last]
		s.index[s.nodes[idx]] = idx
	}
	s.nodes = s.nodes[:last]
	delete(s.index, h)
	rv.size.Add(-1)
}

// Contains returns true if the hash is in the view.
func (rv *RoutableView) Contains(h node.Hash) bool {
	s := &rv.shards[shardFor(h)]
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.index[h]
	return ok
}

// Size returns the total number of hashes across all shards.
func (rv *RoutableView) Size() int {
	return int(rv.size.Load())
}

// Clear removes all entries from all shards.
func (rv *RoutableView) Clear() {
	for i := range rv.shards {
		s := &rv.shards[i]
		s.mu.Lock()
		s.nodes = s.nodes[:0]
		s.index = make(map[node.Hash]int)
		s.mu.Unlock()
	}
	rv.size.Store(0)
}

// RandomPick selects a random hash from the view.
// Returns ok=false if the view is empty.
func (rv *RoutableView) RandomPick(rng *rand.Rand) (node.Hash, bool) {
	total := rv.Size()
	if total == 0 {
		return node.Zero, false
	}

	target := rng.IntN(total)
	for i := range rv.shards {
		s := &rv.shards[i]
		s.mu.RLock()
		n := len(s.nodes)
		if target < n {
			h := s.nodes[target]
			s.mu.RUnlock()
			return h, true
		}
		target -= n
		s.mu.RUnlock()
	}
	return node.Zero, false
}

// Range calls fn for each hash in the view. If fn returns false, iteration stops.
func (rv *RoutableView) Range(fn func(node.Hash) bool) {
	for i := range rv.shards {
		s := &rv.shards[i]
		s.mu.RLock()
		for _, h := range s.nodes {
			if !fn(h) {
				s.mu.RUnlock()
				return
			}
		}
		s.mu.RUnlock()
	}
}
