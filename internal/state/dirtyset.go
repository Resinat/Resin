package state

import "sync"

// DirtyOp represents the type of dirty operation.
type DirtyOp int

const (
	// OpUpsert marks a key for upsert (value read from memory at flush time).
	OpUpsert DirtyOp = iota
	// OpDelete marks a key for deletion.
	OpDelete
)

// DirtySet tracks dirty keys with their operation type.
// It stores only keys — values are read from memory at flush time.
// Thread-safe via mutex; drain uses map-swap for a stable snapshot.
type DirtySet[K comparable] struct {
	mu sync.Mutex
	m  map[K]DirtyOp
}

// NewDirtySet creates an empty DirtySet.
func NewDirtySet[K comparable]() *DirtySet[K] {
	return &DirtySet[K]{m: make(map[K]DirtyOp)}
}

// MarkUpsert marks a key for upsert.
func (d *DirtySet[K]) MarkUpsert(key K) {
	d.mu.Lock()
	d.m[key] = OpUpsert
	d.mu.Unlock()
}

// MarkDelete marks a key for deletion.
func (d *DirtySet[K]) MarkDelete(key K) {
	d.mu.Lock()
	d.m[key] = OpDelete
	d.mu.Unlock()
}

// Drain atomically swaps the internal map with a fresh one and returns the
// old map as a stable snapshot. Concurrent marks after Drain go into the new map.
func (d *DirtySet[K]) Drain() map[K]DirtyOp {
	d.mu.Lock()
	old := d.m
	d.m = make(map[K]DirtyOp, len(old)/2)
	d.mu.Unlock()
	return old
}

// Merge re-merges a previously drained snapshot back into the dirty set.
// Used for flush-failure recovery. Only keys that have NOT been re-dirtied
// since the drain are restored, preserving newer marks.
func (d *DirtySet[K]) Merge(old map[K]DirtyOp) {
	d.mu.Lock()
	for k, v := range old {
		if _, exists := d.m[k]; !exists {
			d.m[k] = v
		}
	}
	d.mu.Unlock()
}

// Len returns the current number of dirty entries.
func (d *DirtySet[K]) Len() int {
	d.mu.Lock()
	n := len(d.m)
	d.mu.Unlock()
	return n
}
