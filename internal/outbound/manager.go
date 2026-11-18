package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
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

func buildOutboundSafely(builder OutboundBuilder, rawOptions json.RawMessage) (ob adapter.Outbound, err error) {
	defer func() {
		if r := recover(); r != nil {
			if ob != nil {
				closeOutbound(ob)
			}
			ob = nil
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return builder.Build(rawOptions)
}

// OutboundPool publishes a built instance under the same per-node operation
// used for role transitions. Construction and disposal stay outside that lock.
type OutboundPool interface {
	PoolAccessor
	ResolveNodeDisplayTag(node.Hash) string
	IsRelayEligible(node.Hash, string) bool
	PublishOutbound(*node.NodeEntry, uint64, *adapter.Outbound, *adapter.Outbound) bool
}

// OutboundManager manages outbound lifecycle and provides unified HTTP execution.
type OutboundManager struct {
	pool      OutboundPool
	builder   OutboundBuilder
	pickRelay func(node.Hash, string) (node.Hash, error)
	lifecycle atomic.Uint64
	drained   chan struct{}
}

func NewOutboundManager(pool OutboundPool, builder OutboundBuilder) *OutboundManager {
	return &OutboundManager{pool: pool, builder: builder, drained: make(chan struct{})}
}

// SetRelayPicker must run before starting outbound/probe workers.
func (m *OutboundManager) SetRelayPicker(pick func(node.Hash, string) (node.Hash, error)) {
	m.pickRelay = pick
}

func (m *OutboundManager) isLiveEntry(hash node.Hash, entry *node.NodeEntry) bool {
	current, ok := m.pool.GetEntry(hash)
	return ok && current == entry
}

// EnsureNodeOutbound builds outside pool locks and publishes with CAS. A stale,
// duplicate, removed or shutdown build is closed instead of being retained.
const managerClosing = uint64(1) << 63

func (m *OutboundManager) beginBuild() bool {
	for {
		state := m.lifecycle.Load()
		if state&managerClosing != 0 {
			return false
		}
		if m.lifecycle.CompareAndSwap(state, state+1) {
			return true
		}
	}
}
func (m *OutboundManager) endBuild() {
	if m.lifecycle.Add(^uint64(0)) == managerClosing {
		close(m.drained)
	}
}
func (m *OutboundManager) isClosed() bool { return m.lifecycle.Load()&managerClosing != 0 }
func (m *OutboundManager) EnsureNodeOutbound(hash node.Hash) {
	if !m.beginBuild() {
		return
	}
	defer m.endBuild()
	entry, ok := m.pool.GetEntry(hash)
	if !ok || m.isClosed() {
		return
	}
	version := entry.RoutingVersion()
	previous := entry.Outbound.Load()
	if previous != nil {
		if n, ok := (*previous).(*nodeOutbound); !ok || (n.version == version && n.Ready()) {
			return
		}
	}
	builder := m.builder
	chain := entry.RoutingRole() == node.RoleChain
	var upstream *chainDialer
	if chain {
		withDetour, ok := builder.(detourBuilder)
		if !ok {
			m.buildError(entry, version, errors.New("builder does not support chain proxy"))
			return
		}
		upstream = &chainDialer{Adapter: newChainAdapter(), manager: m, landing: hash}
		builder = builderFunc(func(raw json.RawMessage) (adapter.Outbound, error) { return withDetour.BuildWithDetour(raw, upstream) })
	}
	native, err := buildOutboundSafely(builder, entry.RawOptions)
	if err != nil {
		m.buildError(entry, version, err)
		return
	}
	n := &nodeOutbound{Outbound: native, version: version, networks: relayNetworks(entry.RawOptions, native), upstream: upstream}
	if chain && native.Type() == "wireguard" {
		n.checkRelay = func() error {
			_, _, err := upstream.pick("udp")
			return err
		}
	}
	var ob adapter.Outbound = n
	if m.isClosed() || !m.isLiveEntry(hash, entry) || entry.RoutingVersion() != version || !m.pool.PublishOutbound(entry, version, previous, &ob) {
		_ = n.Close()
		return
	}
	if previous != nil {
		closeOutbound(*previous)
	}
	if m.isClosed() || !m.isLiveEntry(hash, entry) || entry.RoutingVersion() != version {
		entry.Outbound.CompareAndSwap(&ob, nil)
		_ = n.Close()
		return
	}
	m.notifyReady(hash)
}
func (m *OutboundManager) buildError(entry *node.NodeEntry, version uint64, err error) {
	if pool, ok := m.pool.(interface {
		ApplyFeedback(node.Hash, uint64, node.Feedback) bool
	}); ok {
		msg := "outbound build: " + err.Error()
		pool.ApplyFeedback(entry.Hash, version, node.Feedback{LastError: &msg})
	} else if m.isLiveEntry(entry.Hash, entry) && entry.RoutingVersion() == version {
		entry.SetLastError("outbound build: " + err.Error())
	}
}

func (m *OutboundManager) notifyReady(hash node.Hash) {
	if pool, ok := m.pool.(interface{ NotifyNodeDirty(node.Hash) }); ok {
		pool.NotifyNodeDirty(hash)
	}
}

func (m *OutboundManager) Close() error {
	previous := m.lifecycle.Or(managerClosing)
	if previous&managerClosing == 0 && previous == 0 {
		close(m.drained)
	}
	<-m.drained
	m.pool.RangeNodes(func(_ node.Hash, entry *node.NodeEntry) bool {
		m.RemoveNodeOutbound(entry)
		return true
	})
	return nil
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
	result, err := netutil.HTTPGetViaOutbound(ctx, *outboundPtr, url, netutil.OutboundHTTPOptions{
		RequireStatusOK: true,
		UserAgent:       userAgent,
	})
	return result.Body, result.Latency, err
}
