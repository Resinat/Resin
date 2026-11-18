package outbound

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

func TestChainServerDialFailureTripsRelayOnly(t *testing.T) {
	for _, fastOpen := range []bool{false, true} {
		t.Run(fmt.Sprintf("TFO=%v", fastOpen), func(t *testing.T) { testChainServerDialFailure(t, fastOpen) })
	}
}

func testChainServerDialFailure(t *testing.T, fastOpen bool) {
	jumpB, landing := newChainHTTPProxy(t), newChainHTTPProxy(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	subs := topology.NewSubscriptionManager()
	landSub := subscription.NewSubscription("landing", "landing", "", true, false)
	landSub.SetChainConfig(true, false)
	jumpSub := subscription.NewSubscription("jump", "jump", "", true, false)
	jumpSub.SetChainConfig(false, true)
	subs.Register(landSub)
	subs.Register(jumpSub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxConsecutiveFailures: func() int { return 2 }})
	builder := newTestSingboxBuilder(t)
	defer builder.Close()
	mgr := NewOutboundManager(pool, builder)
	defer mgr.Close()
	add := func(sub *subscription.Subscription, raw []byte) *node.NodeEntry {
		hash := node.HashFromRawOptions(raw)
		sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{})
		pool.AddNodeFromSub(hash, raw, sub.ID)
		mgr.EnsureNodeOutbound(hash)
		pool.RecordResult(hash, true)
		entry, _ := pool.GetEntry(hash)
		return entry
	}
	l := add(landSub, landing.raw(t))
	// Port zero cannot be reclaimed by another test's listener after closing.
	a := add(jumpSub, []byte(fmt.Sprintf(`{"type":"http","server":"127.0.0.1","server_port":0,"tcp_fast_open":%v}`, fastOpen)))
	b := add(jumpSub, jumpB.raw(t))
	selector := routing.NewRelaySelector(pool, nil, nil)
	mgr.SetRelayPicker(func(hash node.Hash, network string) (node.Hash, error) {
		if pool.IsRelayEligible(a.Hash, network) {
			return a.Hash, nil
		}
		return selector.Pick(hash, network)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for attempt := 1; attempt <= 2; attempt++ {
		_, _, err := mgr.Fetch(ctx, l.Hash, target.URL)
		var relay *node.RelayError
		if !errors.As(err, &relay) || !relay.ServerDial || relay.Hash != a.Hash || relay.Version != a.RoutingVersion() {
			t.Fatalf("missing exact first-hop evidence: %v", err)
		}
		failed := false
		pool.ApplyFeedback(l.Hash, l.RoutingVersion(), node.Feedback{Result: &failed, Err: err})
		if l.FailureCount.Load() != 0 || l.IsCircuitOpen() || a.FailureCount.Load() != int32(attempt) {
			t.Fatalf("attempt %d: landing=%d relay=%d", attempt, l.FailureCount.Load(), a.FailureCount.Load())
		}
		if a.IsCircuitOpen() != (attempt == 2) {
			t.Fatal("binding did not honor the existing failure threshold")
		}
	}
	if !b.IsHealthy() || landing.hits.Load() != 0 {
		t.Fatal("first-hop failures changed unrelated health or reached landing")
	}
	body, _, err := mgr.Fetch(ctx, l.Hash, target.URL)
	if err != nil || string(body) != "ok" || jumpB.hits.Load() != 1 || landing.hits.Load() != 1 {
		t.Fatalf("binding failed to switch after relay circuit opened: %q %v", body, err)
	}
}

type failingServerDNS struct {
	adapter.DNSRouter
	err error
}

func (d *failingServerDNS) Lookup(_ context.Context, domain string, _ adapter.DNSQueryOptions) ([]netip.Addr, error) {
	if d.err != nil {
		return nil, d.err
	}
	return nil, &net.DNSError{Err: "no such host", Name: domain, IsNotFound: true}
}

func TestRelayDNSFailureIsNotServerConnectEvidence(t *testing.T) {
	for _, cause := range []error{nil, &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}, Err: os.ErrDeadlineExceeded}} {
		t.Run(fmt.Sprint(cause), func(t *testing.T) { testRelayDNSFailure(t, cause) })
	}
}

func testRelayDNSFailure(t *testing.T, cause error) {
	builder := newTestSingboxBuilder(t)
	defer builder.Close()
	service.MustRegister[adapter.DNSRouter](builder.ctx, &failingServerDNS{DNSRouter: builder.dnsRouter, err: cause})
	ob, err := builder.Build([]byte(`{"type":"http","server":"relay.invalid","server_port":8080}`))
	if err != nil {
		t.Fatal(err)
	}
	defer closeOutbound(ob)
	_, err = ob.DialContext(context.Background(), "tcp", M.ParseSocksaddr("127.0.0.1:80"))
	var dns *net.DNSError
	var serverDial *serverDialError
	if !errors.As(err, &dns) || errors.As(err, &serverDial) {
		t.Fatalf("DNS failure was treated as proven TCP connect failure: %v", err)
	}
	if cause != nil && (!os.IsTimeout(err) || !errors.Is(err, cause)) {
		t.Fatalf("resolver marker lost timeout or cause: %v", err)
	}
}

func TestRelayHandshakeFailureIsNotServerDialFailure(t *testing.T) {
	builder := newTestSingboxBuilder(t)
	defer builder.Close()
	// An HTTP rejection comes after successful first-hop establishment.
	reject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", 403) }))
	defer reject.Close()
	jump := &chainHTTPProxy{server: reject}
	relay, err := builder.Build(jump.raw(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeOutbound(relay)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = relay.DialContext(ctx, "tcp", M.ParseSocksaddr("127.0.0.1:80"))
	var serverDial *serverDialError
	if err == nil || errors.As(err, &serverDial) {
		t.Fatalf("proxy protocol rejection was attributed to server dial: %v", err)
	}
}

func TestRelayErrorPreservesTimeoutCancellationAndConnectionIdentity(t *testing.T) {
	for _, cause := range []error{os.ErrDeadlineExceeded, context.DeadlineExceeded} {
		err := relayError(node.Hash{1}, 7, &serverDialError{cause})
		if !os.IsTimeout(err) || !errors.Is(err, cause) {
			t.Fatalf("lost timeout or wrapped cause: %v", err)
		}
	}
	if err := relayError(node.Hash{1}, 7, context.Canceled); err != context.Canceled {
		t.Fatalf("client cancellation was marked as relay failure: %v", err)
	}
	// Later I/O is associated with the connection's relay, not a new binding.
	c := &relayConn{info: node.RelayInfo{Hash: node.Hash{1}, Version: 7}}
	var relay *node.RelayError
	err := c.wrapError(fmt.Errorf("protocol stream: %w", io.ErrUnexpectedEOF))
	if !errors.As(err, &relay) || relay.Hash != (node.Hash{1}) || relay.Version != 7 || relay.ServerDial {
		t.Fatalf("incorrect deferred error evidence: %v", err)
	}
	known := &node.RelayError{Hash: node.Hash{2}, Version: 8, ServerDial: true, Err: os.ErrDeadlineExceeded}
	if err := c.info.WrapError(known); err != known {
		t.Fatal("connection metadata overwrote explicit failure evidence")
	}
	for _, cause := range []error{io.EOF, net.ErrClosed} {
		if err := c.wrapError(cause); err != cause {
			t.Fatalf("normal connection close was marked as relay failure: %v", err)
		}
	}
}
