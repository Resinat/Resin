package outbound

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

type bindingIdentityPool struct {
	*topology.GlobalNodePool
	beforeEligibility func()
}

func (p *bindingIdentityPool) IsRelayEligible(h node.Hash, network string) bool {
	if f := p.beforeEligibility; f != nil {
		p.beforeEligibility = nil
		f()
	}
	return p.GlobalNodePool.IsRelayEligible(h, network)
}

type bindingIdentityOutbound struct {
	adapter.Outbound
	calls atomic.Int64
}

func (o *bindingIdentityOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	o.calls.Add(1)
	return nil, errors.New("instrumented dial")
}

func TestChainBindingSameHashReplacementCannotAuthorizeOldEntry(t *testing.T) {
	subs := topology.NewSubscriptionManager()
	sub := subscription.NewSubscription("audit-relay", "audit-relay", "", true, false)
	sub.SetChainConfig(false, true)
	subs.Register(sub)
	base := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subs.Lookup,
		MaxConsecutiveFailures: func() int { return 1 },
	})
	pool := &bindingIdentityPool{GlobalNodePool: base}
	manager := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	defer manager.Close()
	raw := []byte(`{"type":"socks","server":"identity-relay"}`)
	hash := node.HashFromRawOptions(raw)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"relay"}})
	add := func() (*node.NodeEntry, *bindingIdentityOutbound) {
		base.AddNodeFromSub(hash, raw, sub.ID)
		e, ok := base.GetEntry(hash)
		if !ok {
			t.Fatal("missing relay")
		}
		native := &bindingIdentityOutbound{Outbound: testutil.NewNoopOutbound()}
		var ob adapter.Outbound = native
		e.Outbound.Store(&ob)
		base.RecordResult(hash, true)
		return e, native
	}
	old, oldNative := add()
	picks := 0
	manager.SetRelayPicker(func(node.Hash, string) (node.Hash, error) {
		picks++
		return hash, nil
	})
	d := &chainDialer{manager: manager, landing: node.Hash{99}}
	if _, _, err := d.pick("tcp"); err != nil {
		t.Fatal(err)
	}
	base.RecordResult(hash, false)
	if old.IsHealthy() {
		t.Fatal("old relay must be unhealthy before the audited dial begins")
	}

	deleted := make(chan struct{})
	releaseRemoval := make(chan struct{})
	removalDone := make(chan struct{})
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(releaseRemoval) })
		select {
		case <-removalDone:
		case <-time.After(time.Second):
			t.Error("removal callback did not finish")
		}
	}()
	base.SetOnNodeRemoved(func(_ node.Hash, e *node.NodeEntry) {
		close(deleted)
		<-releaseRemoval
		manager.RemoveNodeOutbound(e)
	})
	var replacement *node.NodeEntry
	var replacementNative *bindingIdentityOutbound
	pool.beforeEligibility = func() {
		// Pause removal between map deletion and native disposal. Eligibility
		// sees the healthy replacement, so the following identity check must
		// reject the old binding rather than borrowing the replacement's health.
		go func() { base.RemoveNodeFromSub(hash, sub.ID); close(removalDone) }()
		select {
		case <-deleted:
		case <-time.After(time.Second):
			t.Fatal("remover did not delete old entry")
		}
		replacement, replacementNative = add()
		if replacement == old || !replacement.IsHealthy() {
			t.Fatal("invalid replacement fixture")
		}
	}
	_, _ = d.DialContext(context.Background(), "tcp", M.ParseSocksaddr("127.0.0.1:80"))
	live, _ := base.GetEntry(hash)
	if oldNative.calls.Load() != 0 || replacementNative.calls.Load() != 1 {
		t.Fatalf("dial used wrong entry: old=%d replacement=%d pickerCalls=%d oldHealthy=%v liveIsOld=%v bindingIsOld=%v; want only replacement dialed",
			oldNative.calls.Load(), replacementNative.calls.Load(), picks, old.IsHealthy(), live == old, d.relay.Load().entry == old)
	}
}
