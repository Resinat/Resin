package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

type bindingTestOutbound struct {
	adapter.Outbound
	networks []string
}

func (n *bindingTestOutbound) Network() []string { return n.networks }

func addBindingRelay(pool *mockPool, name string, networks ...string) *node.NodeEntry {
	e := newTestEntry(`{"type":"socks","server":"` + name + `"}`)
	e.UpdateRoutingRole(node.RoleRelay)
	var ob adapter.Outbound = &bindingTestOutbound{Outbound: testutil.NewNoopOutbound(), networks: networks}
	e.Outbound.Store(&ob)
	pool.addEntry(e)
	return e
}

func TestChainBindingReusesHealthyRelay(t *testing.T) {
	pool := new(mockPool)
	a := addBindingRelay(pool, "a", "tcp", "udp")
	b := addBindingRelay(pool, "b", "tcp", "udp")
	manager := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	defer manager.Close()
	var selected atomic.Pointer[node.NodeEntry]
	selected.Store(a)
	var calls atomic.Int64
	manager.SetRelayPicker(func(node.Hash, string) (node.Hash, error) { calls.Add(1); return selected.Load().Hash, nil })
	d := &chainDialer{manager: manager, landing: node.Hash{99}}
	if _, hash, err := d.pick("tcp"); err != nil || hash.hash() != a.Hash {
		t.Fatalf("initial binding: %s %v", hash.hash(), err)
	}
	selected.Store(b)
	for i := 0; i < 32; i++ {
		if _, hash, err := d.pick("tcp4"); err != nil || hash.hash() != a.Hash {
			t.Fatalf("healthy binding changed: %s %v", hash.hash(), err)
		}
	}
	// A failed end-to-end dial is not evidence that the relay is unhealthy.
	if _, err := d.DialContext(context.Background(), "tcp", M.ParseSocksaddr("127.0.0.1:80")); err == nil {
		t.Fatal("noop fixture should fail its dial")
	}
	if d.relay.Load().entry != a || calls.Load() != 1 {
		t.Fatal("ordinary dial failure or new candidate replaced healthy binding")
	}
	// Each landing owns its binding; a second instance can select B.
	other := &chainDialer{manager: manager, landing: node.Hash{100}}
	if _, hash, err := other.pick("tcp"); err != nil || hash.hash() != b.Hash {
		t.Fatalf("independent landing: %s %v", hash.hash(), err)
	}
}

func TestBoundRelaySnapshotDoesNotSelectOrRetainInvalidBinding(t *testing.T) {
	for _, reason := range []string{"unhealthy", "role", "removed", "replaced", "incompatible", "closed"} {
		t.Run(reason, func(t *testing.T) {
			pool := new(mockPool)
			relay := addBindingRelay(pool, "relay", "tcp")
			manager := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
			defer manager.Close()
			picks := 0
			manager.SetRelayPicker(func(node.Hash, string) (node.Hash, error) {
				picks++
				return relay.Hash, nil
			})
			d := &chainDialer{manager: manager, landing: node.Hash{99}}
			n := &nodeOutbound{upstream: d}
			if !n.BoundRelay().IsZero() || picks != 0 {
				t.Fatal("reading an unassigned relay selected a candidate")
			}
			if _, _, err := d.pick("tcp"); err != nil {
				t.Fatal(err)
			}
			if n.BoundRelay() != relay.Hash || picks != 1 {
				t.Fatal("reading an assigned relay changed the binding")
			}
			switch reason {
			case "unhealthy":
				relay.CircuitOpenSince.Store(1)
			case "role":
				relay.UpdateRoutingRole(node.RoleOrdinary)
			case "removed":
				pool.removeEntry(relay.Hash)
			case "replaced":
				addBindingRelay(pool, "relay", "tcp")
			case "incompatible":
				var ob adapter.Outbound = &bindingTestOutbound{Outbound: testutil.NewNoopOutbound(), networks: []string{"udp"}}
				relay.Outbound.Store(&ob)
			case "closed":
				n.closed.Store(true)
			}
			if !n.BoundRelay().IsZero() || picks != 1 {
				t.Fatal("invalid binding was shown or snapshot reselected a candidate")
			}
		})
	}
}

func TestChainBindingReplacesInvalidRelay(t *testing.T) {
	for _, reason := range []string{"unhealthy", "role", "removed", "same hash replaced", "incompatible"} {
		t.Run(reason, func(t *testing.T) {
			pool := new(mockPool)
			a := addBindingRelay(pool, "a", "tcp")
			b := addBindingRelay(pool, "b", "tcp", "udp")
			manager := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
			defer manager.Close()
			var selected atomic.Pointer[node.NodeEntry]
			selected.Store(a)
			var calls atomic.Int64
			manager.SetRelayPicker(func(node.Hash, string) (node.Hash, error) { calls.Add(1); return selected.Load().Hash, nil })
			d := &chainDialer{manager: manager, landing: node.Hash{99}}
			if _, _, err := d.pick("tcp"); err != nil {
				t.Fatal(err)
			}
			expected := b
			network := "tcp"
			switch reason {
			case "unhealthy":
				a.CircuitOpenSince.Store(time.Now().UnixNano())
			case "role":
				a.UpdateRoutingRole(node.RoleOrdinary)
			case "removed":
				pool.removeEntry(a.Hash)
			case "same hash replaced":
				expected = addBindingRelay(pool, "a", "tcp")
			case "incompatible":
				network = "udp"
			}
			selected.Store(expected)
			ob, hash, err := d.pick(network)
			if err != nil || hash.hash() != expected.Hash || ob != *expected.Outbound.Load() || d.relay.Load().entry != expected {
				t.Fatalf("invalid %s binding retained: %s %v", reason, hash.hash(), err)
			}
			selected.Store(a)
			if _, hash, err := d.pick("tcp"); err != nil || hash.hash() != expected.Hash {
				t.Fatalf("new healthy binding was not retained: %s %v", hash.hash(), err)
			}
			if calls.Load() != 2 {
				t.Fatalf("selection calls=%d want 2", calls.Load())
			}
		})
	}
}

func TestChainBindingConcurrentFirstDialsUseOnePublishedChoice(t *testing.T) {
	pool := new(mockPool)
	a := addBindingRelay(pool, "a", "tcp")
	b := addBindingRelay(pool, "b", "tcp")
	manager := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	defer manager.Close()
	const count = 16
	entered := make(chan struct{}, count)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var calls atomic.Int64
	manager.SetRelayPicker(func(node.Hash, string) (node.Hash, error) {
		n := calls.Add(1)
		entered <- struct{}{}
		<-release
		if n%2 == 0 {
			return a.Hash, nil
		}
		return b.Hash, nil
	})
	d := &chainDialer{manager: manager, landing: node.Hash{99}}
	type result struct {
		hash node.Hash
		err  error
	}
	results := make(chan result, count)
	for i := 0; i < count; i++ {
		go func() { _, hash, err := d.pick("tcp"); results <- result{hash.hash(), err} }()
	}
	// Hold all independently chosen candidates until every dial has reached the
	// selector. Only the CAS winner may determine the relay used by these dials.
	for i := 0; i < count; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("concurrent selectors failed to enter")
		}
	}
	releaseOnce.Do(func() { close(release) })
	var winner node.Hash
	for i := 0; i < count; i++ {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if i == 0 {
				winner = result.hash
			} else if result.hash != winner {
				t.Fatal("concurrent first dials used different relays")
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent binding did not converge")
		}
	}
	if calls.Load() != count {
		t.Fatal("CAS losers selected again instead of reusing the winning binding")
	}
}

func TestChainBindingUnavailableRelayDoesNotFallBackToDirect(t *testing.T) {
	pool := new(mockPool)
	a := addBindingRelay(pool, "a", "tcp")
	manager := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	defer manager.Close()
	manager.SetRelayPicker(func(node.Hash, string) (node.Hash, error) {
		if a.IsHealthy() {
			return a.Hash, nil
		}
		return node.Zero, routing.ErrNoAvailableRelay
	})
	d := &chainDialer{manager: manager, landing: node.Hash{99}}
	if _, _, err := d.pick("tcp"); err != nil {
		t.Fatal(err)
	}
	a.CircuitOpenSince.Store(1)
	if ob, hash, err := d.pick("tcp"); ob != nil || !hash.hash().IsZero() || !errors.Is(err, routing.ErrNoAvailableRelay) {
		t.Fatalf("unavailable binding: %v %s %v", ob, hash.hash(), err)
	}
}

type bindingWireguardBuilder struct{ upstream adapter.Outbound }

func (*bindingWireguardBuilder) Build(json.RawMessage) (adapter.Outbound, error) {
	return testutil.NewNoopOutbound(), nil
}
func (b *bindingWireguardBuilder) BuildWithDetour(_ json.RawMessage, upstream adapter.Outbound) (adapter.Outbound, error) {
	b.upstream = upstream
	return bindingWireguardOutbound{testutil.NewNoopOutbound()}, nil
}

type bindingWireguardOutbound struct{ adapter.Outbound }

func (bindingWireguardOutbound) Type() string { return "wireguard" }

func TestWireguardPrecheckAndTransportShareRelayBinding(t *testing.T) {
	pool := new(mockPool)
	a := addBindingRelay(pool, "a", "udp")
	b := addBindingRelay(pool, "b", "udp")
	landing := newTestEntry(`{"type":"wireguard","server":"landing"}`)
	landing.UpdateRoutingRole(node.RoleChain)
	pool.addEntry(landing)
	builder := new(bindingWireguardBuilder)
	manager := NewOutboundManager(pool, builder)
	defer manager.Close()
	selected := a
	calls := 0
	manager.SetRelayPicker(func(node.Hash, string) (node.Hash, error) { calls++; return selected.Hash, nil })
	manager.EnsureNodeOutbound(landing.Hash)
	n := (*landing.Outbound.Load()).(*nodeOutbound)
	if n.checkRelay == nil {
		t.Fatal("WireGuard precheck missing")
	}
	if err := n.checkRelay(); err != nil {
		t.Fatal(err)
	}
	selected = b
	d := builder.upstream.(*chainDialer)
	if _, hash, err := d.pick("udp"); err != nil || hash.hash() != a.Hash {
		t.Fatalf("transport ignored precheck binding: %s %v", hash.hash(), err)
	}
	if err := n.checkRelay(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("precheck and transport selected independently: %d", calls)
	}
}

func TestChainBindingLateSelectionCannotOverwriteReboundRelay(t *testing.T) {
	pool := new(mockPool)
	a := addBindingRelay(pool, "a", "tcp")
	b := addBindingRelay(pool, "b", "tcp")
	c := addBindingRelay(pool, "c", "tcp")
	manager := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	defer manager.Close()
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var calls atomic.Int64
	manager.SetRelayPicker(func(node.Hash, string) (node.Hash, error) {
		switch calls.Add(1) {
		case 1:
			return a.Hash, nil
		case 2:
			close(started)
			<-release
			return c.Hash, nil
		case 3:
			return b.Hash, nil
		default:
			return a.Hash, nil
		}
	})
	d := &chainDialer{manager: manager, landing: node.Hash{99}}
	if _, hash, err := d.pick("tcp"); err != nil || hash.hash() != a.Hash {
		t.Fatalf("initial binding: %s %v", hash.hash(), err)
	}
	initial := d.relay.Load()
	a.CircuitOpenSince.Store(1)
	type result struct {
		hash node.Hash
		err  error
	}
	done := make(chan result, 1)
	go func() { _, hash, err := d.pick("tcp"); done <- result{hash.hash(), err} }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("late selector did not start")
	}
	if _, hash, err := d.pick("tcp"); err != nil || hash.hash() != b.Hash {
		t.Fatalf("first replacement: %s %v", hash.hash(), err)
	}
	a.CircuitOpenSince.Store(0)
	b.CircuitOpenSince.Store(1)
	if _, hash, err := d.pick("tcp"); err != nil || hash.hash() != a.Hash {
		t.Fatalf("second replacement: %s %v", hash.hash(), err)
	}
	rebound := d.relay.Load()
	if rebound == initial {
		t.Fatal("reselecting a node reused the old binding identity")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case result := <-done:
		if result.err != nil || result.hash != a.Hash || d.relay.Load() != rebound {
			t.Fatalf("late selector overwrote current healthy binding: %s %v", result.hash, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("late selector failed to reuse the rebound relay")
	}
	if calls.Load() != 4 {
		t.Fatal("late selector ran P2C again instead of reusing the healthy binding")
	}
}
