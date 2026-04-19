package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
)

var ErrOutboundNotReady = errors.New("outbound not ready")

// PoolAccessor provides read-only access to the node pool.
type PoolAccessor interface {
	GetEntry(hash node.Hash) (*node.NodeEntry, bool)
	RangeNodes(fn func(node.Hash, *node.NodeEntry) bool)
}

// closeOutbound closes an outbound if it implements io.Closer.
func closeOutbound(ob adapter.Outbound) {
	if c, ok := ob.(io.Closer); ok {
		_ = c.Close()
	}
}

// OutboundManager manages outbound lifecycle and provides unified HTTP execution.
type OutboundManager struct {
	pool    PoolAccessor
	builder OutboundBuilder
}

func NewOutboundManager(pool PoolAccessor, builder OutboundBuilder) *OutboundManager {
	return &OutboundManager{pool: pool, builder: builder}
}

func (m *OutboundManager) isLiveEntry(hash node.Hash, entry *node.NodeEntry) bool {
	current, ok := m.pool.GetEntry(hash)
	return ok && current == entry
}

// EnsureNodeOutbound idempotently creates and stores an outbound for a node.
// Uses CompareAndSwap(nil, &wrapped) to guarantee only one goroutine's build
// result is stored. Losers discard their result (stage 6 adds io.Closer release).
func (m *OutboundManager) EnsureNodeOutbound(hash node.Hash) {
	m.ensureNodeOutbound(hash, map[node.Hash]struct{}{})
}

func (m *OutboundManager) ensureNodeOutbound(hash node.Hash, visiting map[node.Hash]struct{}) {
	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return
	}
	// Fast path: already has outbound.
	if entry.Outbound.Load() != nil {
		return
	}
	if _, exists := visiting[hash]; exists {
		entry.SetLastError("outbound build: detour cycle detected")
		return
	}
	visiting[hash] = struct{}{}
	defer delete(visiting, hash)

	if detourTag, ok := parseOutboundDetourTag(entry.RawOptions); ok {
		log.Printf("[outbound] ensure node=%s detour=%s", hash.Hex(), detourTag)
		detourHash, found := m.findHashByOutboundTag(detourTag)
		if !found {
			log.Printf("[outbound] detour target missing node=%s detour=%s", hash.Hex(), detourTag)
			entry.SetLastError("outbound build: detour target not found: " + detourTag)
			return
		}
		m.ensureNodeOutbound(detourHash, visiting)
		detourEntry, ok := m.pool.GetEntry(detourHash)
		if !ok || detourEntry.Outbound.Load() == nil {
			log.Printf("[outbound] detour target not ready node=%s detour=%s detour_hash=%s", hash.Hex(), detourTag, detourHash.Hex())
			entry.SetLastError("outbound build: detour target not ready: " + detourTag)
			return
		}
		log.Printf("[outbound] detour target ready node=%s detour=%s detour_hash=%s", hash.Hex(), detourTag, detourHash.Hex())
	}

	ob, err := m.builder.Build(entry.RawOptions)
	if err != nil {
		entry.SetLastError("outbound build: " + err.Error())
		return
	}
	entry.SetLastError("")

	// Build can race with node deletion/replacement. If this entry is no longer
	// the pool's live value for the hash, discard the build result.
	if !m.isLiveEntry(hash, entry) {
		closeOutbound(ob)
		return
	}

	if !entry.Outbound.CompareAndSwap(nil, &ob) {
		// Another goroutine won the race. Close the losing build result.
		closeOutbound(ob)
		return
	}

	// Close-and-clear if the node disappeared/replaced right after CAS.
	if !m.isLiveEntry(hash, entry) {
		old := entry.Outbound.Swap(nil)
		if old != nil {
			closeOutbound(*old)
		}
	}
}

// RemoveNodeOutbound clears a node's outbound reference.
// Accepts the entry directly because the node may already be deleted from the pool
// (RemoveNodeFromSub deletes before firing onNodeRemoved callback).
func (m *OutboundManager) RemoveNodeOutbound(entry *node.NodeEntry) {
	if entry == nil {
		return
	}
	old := entry.Outbound.Swap(nil)
	if old != nil {
		closeOutbound(*old)
	}
}

// WarmupAll iterates all nodes in the pool and ensures each has an outbound.
// Called once after bootstrap to avoid ErrOutboundNotReady on restart.
func (m *OutboundManager) WarmupAll() {
	m.pool.RangeNodes(func(h node.Hash, _ *node.NodeEntry) bool {
		m.EnsureNodeOutbound(h)
		return true
	})
}

// Fetch executes HTTP request using the node's outbound.
// Returns ErrOutboundNotReady if the node's outbound is not yet initialized.
// ctx controls timeout/cancellation.
func (m *OutboundManager) Fetch(ctx context.Context, hash node.Hash, url string) ([]byte, time.Duration, error) {
	return m.FetchWithUserAgent(ctx, hash, url, "")
}

// FetchWithUserAgent executes HTTP request using the node's outbound and
// applies the given User-Agent if non-empty.
func (m *OutboundManager) FetchWithUserAgent(
	ctx context.Context,
	hash node.Hash,
	url string,
	userAgent string,
) ([]byte, time.Duration, error) {
	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return nil, 0, errors.New("node not found")
	}
	outboundPtr := entry.Outbound.Load() // *adapter.Outbound
	if outboundPtr == nil {
		return nil, 0, ErrOutboundNotReady
	}
	return netutil.HTTPGetViaOutbound(ctx, *outboundPtr, url, netutil.OutboundHTTPOptions{
		RequireStatusOK: true,
		UserAgent:       userAgent,
	})
}

func parseOutboundDetourTag(raw json.RawMessage) (string, bool) {
	var outbound map[string]any
	if err := json.Unmarshal(raw, &outbound); err != nil {
		return "", false
	}
	rawDetour, ok := outbound["detour"]
	if !ok {
		return "", false
	}
	detour, ok := rawDetour.(string)
	if !ok || detour == "" {
		return "", false
	}
	return detour, true
}

func parseOutboundTag(raw json.RawMessage) (string, error) {
	var outbound map[string]any
	if err := json.Unmarshal(raw, &outbound); err != nil {
		return "", fmt.Errorf("parse outbound: %w", err)
	}
	rawTag, ok := outbound["tag"]
	if !ok {
		return "", errors.New("tag missing")
	}
	tag, ok := rawTag.(string)
	if !ok || tag == "" {
		return "", errors.New("tag invalid")
	}
	return tag, nil
}

func (m *OutboundManager) findHashByOutboundTag(tag string) (node.Hash, bool) {
	found := node.Zero
	matched := false
	m.pool.RangeNodes(func(h node.Hash, entry *node.NodeEntry) bool {
		if entry == nil {
			return true
		}
		outboundTag, err := parseOutboundTag(entry.RawOptions)
		if err != nil {
			return true
		}
		if outboundTag == tag {
			found = h
			matched = true
			return false
		}
		return true
	})
	return found, matched
}
