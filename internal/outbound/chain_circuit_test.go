package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

func TestChainBindingDoesNotReviveAfterEitherCircuit(t *testing.T) {
	for _, which := range []string{"landing", "relay"} {
		t.Run(which, func(t *testing.T) {
			pool, landing, relay := newChainFeedbackPool(t)
			n := (*landing.Outbound.Load()).(*nodeOutbound)
			_, before, err := n.upstream.pick("tcp")
			if err != nil || n.BoundRelay() != relay.Hash {
				t.Fatalf("initial binding: %v", err)
			}
			broken := landing
			if which == "relay" {
				broken = relay
			}
			generation := broken.CircuitGeneration.Load()
			pool.RecordResult(broken.Hash, false)
			if n.BoundRelay() != relay.Hash {
				t.Fatal("a failure below the threshold invalidated the binding")
			}
			pool.RecordResult(broken.Hash, false)
			if !n.BoundRelay().IsZero() || broken.CircuitGeneration.Load() != generation+1 {
				t.Fatal("circuit opening left the old binding valid")
			}
			pool.RecordResult(broken.Hash, true)
			if !n.BoundRelay().IsZero() {
				t.Fatal("recovery revived a binding invalidated by circuit opening")
			}
			_, after, err := n.upstream.pick("tcp")
			if err != nil || before == after || after.hash() != relay.Hash {
				t.Fatalf("next dial did not select a new binding generation: %v", err)
			}
		})
	}
}

func TestChainRecoveryFailureRevokesItsOwnBinding(t *testing.T) {
	pool, landing, relay := newChainFeedbackPool(t)
	n := (*landing.Outbound.Load()).(*nodeOutbound)
	pool.RecordResult(landing.Hash, false)
	pool.RecordResult(landing.Hash, false)
	if !landing.IsCircuitOpen() {
		t.Fatal("fixture must start circuit-open")
	}
	var truncate atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if truncate.Load() {
			w.Header().Set("Content-Length", "100")
			io.WriteString(w, "short")
		} else {
			io.WriteString(w, "ok")
		}
	}))
	defer target.Close()
	fetch := func() (netutil.OutboundHTTPResult, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return netutil.HTTPGetViaOutbound(ctx, n, target.URL, netutil.OutboundHTTPOptions{})
	}
	truncate.Store(true)
	result, err := fetch()
	if !errors.Is(err, io.ErrUnexpectedEOF) || result.Relay.Binding == nil || n.BoundRelay() != relay.Hash {
		t.Fatalf("failed recovery did not carry its binding: %v", err)
	}
	old := n.upstream.relay.Load()
	failed := false
	feedback := node.Feedback{Result: &failed, Err: err, Relay: result.Relay}
	pool.ApplyFeedback(landing.Hash, landing.RoutingVersion(), feedback)
	if !landing.IsCircuitOpen() || !n.BoundRelay().IsZero() || relay.IsCircuitOpen() {
		t.Fatal("failed recovery retained its binding or blamed the healthy relay")
	}
	// A new attempt can bind while the landing is still open. Late feedback
	// from the previous attempt must not revoke this newer generation.
	_, replacement, err := n.upstream.pick("tcp")
	if err != nil || old == replacement {
		t.Fatalf("recovery could not create a replacement binding: %v", err)
	}
	pool.ApplyFeedback(landing.Hash, landing.RoutingVersion(), feedback)
	if n.upstream.relay.Load() != replacement || n.BoundRelay() != relay.Hash {
		t.Fatal("late failure revoked the newer binding")
	}
	truncate.Store(false)
	result, err = fetch()
	if err != nil {
		t.Fatal(err)
	}
	success := true
	pool.ApplyFeedback(landing.Hash, landing.RoutingVersion(), node.Feedback{Result: &success, Relay: result.Relay})
	if landing.IsCircuitOpen() || n.upstream.relay.Load() != replacement {
		t.Fatal("successful recovery did not retain the replacement binding")
	}
}

func TestDelayedSelectionDoesNotCrossCircuitGeneration(t *testing.T) {
	pool, landing, relay := newChainFeedbackPool(t)
	n := (*landing.Outbound.Load()).(*nodeOutbound)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	n.upstream.manager.SetRelayPicker(func(node.Hash, string) (node.Hash, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return relay.Hash, nil
	})
	type result struct {
		binding *relayBinding
		err     error
	}
	done := make(chan result, 1)
	go func() { _, binding, err := n.upstream.pick("tcp"); done <- result{binding, err} }()
	<-started
	if n.upstream.relay.Load() != nil {
		t.Fatal("an attempt published an empty binding before selection")
	}
	pool.RecordResult(landing.Hash, false)
	pool.RecordResult(landing.Hash, false)
	_, recovery, err := n.upstream.pick("tcp")
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case old := <-done:
		if old.err != nil || old.binding != recovery || n.upstream.relay.Load() != recovery {
			t.Fatalf("delayed selection reused a pre-circuit generation: %v", old.err)
		}
	case <-time.After(time.Second):
		t.Fatal("delayed selection did not finish")
	}
}

type circuitFailureOutbound struct {
	adapter.Outbound
	dial func(context.Context) error
	conn net.Conn
}

func (n *circuitFailureOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	if n.dial == nil {
		return n.conn, nil
	}
	return nil, n.dial(ctx)
}

func (n *circuitFailureOutbound) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	return nil, n.dial(ctx)
}

func TestFirstRecoveryDialFailureCarriesBindingWithoutRelayIdentity(t *testing.T) {
	for _, failure := range []string{"protocol", "first hop", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			pool, landing, relay := newChainFeedbackPool(t)
			original := (*landing.Outbound.Load()).(*nodeOutbound)
			pool.RecordResult(landing.Hash, false)
			pool.RecordResult(landing.Hash, false)
			n := &nodeOutbound{upstream: original.upstream, version: original.version}
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			defer target.Close()
			n.Outbound = &circuitFailureOutbound{Outbound: original.Outbound, dial: func(ctx context.Context) error {
				// The detour runs with this call's context, but the native protocol
				// then returns a handshake error without exposing the connection.
				conn, err := original.upstream.DialContext(ctx, "tcp", M.ParseSocksaddr(target.Listener.Addr().String()))
				if err != nil {
					return err
				}
				conn.Close()
				switch failure {
				case "first hop":
					return &node.RelayError{Hash: relay.Hash, Version: relay.RoutingVersion(), ServerDial: true, Err: errors.New("connection refused")}
				case "canceled":
					return context.Canceled
				default:
					return errors.New("protocol handshake rejected")
				}
			}}
			pool.SetOnRelaySuspect(func(node.Hash) { t.Fatal("binding metadata triggered an extra probe") })
			_, err := n.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.com:443"))
			if err == nil {
				t.Fatal("fixture must fail")
			}
			failed := false
			pool.ApplyFeedback(landing.Hash, landing.RoutingVersion(), node.Feedback{Result: &failed, Err: err})
			if failure == "canceled" {
				if !errors.Is(err, context.Canceled) || n.BoundRelay() != relay.Hash {
					t.Fatal("cancellation invalidated the binding")
				}
			} else if node.BindingFromError(err) == nil || !n.BoundRelay().IsZero() {
				t.Fatal("failed first recovery attempt retained its binding")
			}
		})
	}
}

type opaqueSessionConn struct {
	net.Conn
	err error
}

func (c *opaqueSessionConn) Read([]byte) (int, error)    { return 0, c.err }
func (c *opaqueSessionConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *opaqueSessionConn) Close() error                { return nil }

func TestUnknownSessionFailureDoesNotRevokeCurrentBinding(t *testing.T) {
	for _, stage := range []string{"dial", "read", "listen"} {
		t.Run(stage, func(t *testing.T) {
			pool, landing, _ := newChainFeedbackPool(t)
			original := (*landing.Outbound.Load()).(*nodeOutbound)
			_, old, err := original.upstream.pick("tcp")
			if err != nil {
				t.Fatal(err)
			}
			pool.RecordResult(landing.Hash, false)
			pool.RecordResult(landing.Hash, false)
			_, current, err := original.upstream.pick("tcp")
			if err != nil || old == current {
				t.Fatalf("replacement binding: %v", err)
			}
			cause := errors.New("old hidden session failed")
			native := &circuitFailureOutbound{Outbound: original.Outbound, dial: func(context.Context) error { return cause }}
			if stage == "read" {
				native.dial = nil
				native.conn = &opaqueSessionConn{err: cause}
			}
			n := &nodeOutbound{Outbound: native, upstream: original.upstream, version: original.version}
			ctx, destination := context.Background(), M.ParseSocksaddr("example.com:443")
			switch stage {
			case "listen":
				_, err = n.ListenPacket(ctx, destination)
			case "read":
				var conn net.Conn
				conn, err = n.DialContext(ctx, "tcp", destination)
				if conn != native.conn {
					t.Fatal("native connection was unnecessarily wrapped")
				}
				_, err = conn.Read(make([]byte, 1))
			default:
				_, err = n.DialContext(ctx, "tcp", destination)
			}
			if !errors.Is(err, cause) || node.BindingFromError(err) != nil {
				t.Fatalf("unknown session borrowed a binding identity: %v", err)
			}
			failed := false
			pool.ApplyFeedback(landing.Hash, landing.RoutingVersion(), node.Feedback{Result: &failed, Err: err})
			if original.upstream.relay.Load() != current {
				t.Fatal("old hidden session revoked the current binding")
			}
		})
	}
}

type hiddenHalfCloseConn struct{ net.Conn }

func (c hiddenHalfCloseConn) Upstream() any { return c.Conn }

type halfCloseCounterConn struct {
	net.Conn
	calls atomic.Int32
}

func (c *halfCloseCounterConn) CloseWrite() error { c.calls.Add(1); return nil }
func (c *halfCloseCounterConn) CloseRead() error  { c.calls.Add(1); return nil }

func TestRelayConnPreservesNativeHalfCloseBoundary(t *testing.T) {
	native := new(halfCloseCounterConn)
	hidden := &relayConn{Conn: hiddenHalfCloseConn{Conn: native}}
	if hidden.CloseWrite() == nil || hidden.CloseRead() == nil || native.calls.Load() != 0 {
		t.Fatal("metadata wrapper exposed half-close hidden by the native protocol")
	}
	direct := &relayConn{Conn: native}
	if direct.CloseWrite() != nil || direct.CloseRead() != nil || native.calls.Load() != 2 {
		t.Fatal("metadata wrapper lost explicit native half-close support")
	}
}
