package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	M "github.com/sagernet/sing/common/metadata"
)

// Same deferred failure contract as the native HTTP/2 outbound, without its
// unsynchronized Setup/Read implementation. All Resin copy/feedback code runs.
func TestDeferredRelayErrorFeedback(t *testing.T) {
	for _, cause := range []error{
		routing.ErrNoAvailableRelay,
		&node.RelayError{Hash: node.Hash{42}, Version: 7, ServerDial: true, Err: errors.New("connection refused")},
		&node.RelayError{Hash: node.Hash{42}, Version: 7, Err: io.ErrUnexpectedEOF},
	} {
		t.Run(cause.Error(), func(t *testing.T) { testDeferredRelayErrorFeedback(t, cause) })
	}
}

func testDeferredRelayErrorFeedback(t *testing.T, cause error) {
	env := newProxyE2EEnv(t)
	var hash node.Hash
	env.pool.RangeNodes(func(h node.Hash, _ *node.NodeEntry) bool { hash = h; return false })
	entry, _ := env.pool.GetEntry(hash)
	ready := make(chan struct{})
	ob := &mockOutbound{dialFunc: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		client, peer := net.Pipe()
		go func() { defer peer.Close(); buffer := make([]byte, 1024); _, _ = peer.Read(buffer); close(ready) }()
		return &deferredRelayConn{Conn: client, ready: ready, err: cause}, nil
	}}
	wrapped := entry.Outbound.Load()
	// The fixture retains the real node's protocol metadata while overriding dial.
	ob.Outbound = *wrapped
	var outboundValue = ob.Outbound
	outboundValue = ob
	entry.Outbound.Store(&outboundValue)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	recorder := &feedbackCapture{events: make(chan node.Feedback, 8)}
	prepared := prepareConnectTunnel(ctx, tunnelDeps{router: env.router, pool: env.pool, health: recorder}, "plat", "", "example.com:443")
	if prepared.session == nil {
		t.Fatalf("prepare failed: %+v", prepared)
	}
	// Drain the initial latency-attempt event before the end-to-end copy result.
	select {
	case <-recorder.events:
	case <-time.After(time.Second):
		t.Fatal("missing initial latency attempt")
	}
	client, peer := net.Pipe()
	defer peer.Close()
	go io.Copy(io.Discard, peer)
	result := pumpPreparedTunnelReader(client, strings.NewReader("hello"), prepared.session, tunnelPumpOptions{requireBidirectionalTraffic: true})
	if !errors.Is(result.upstreamErr, cause) || result.proxyErr != classifyUpstreamError(cause) {
		t.Fatalf("result: %+v", result)
	}
	prepared.session.recordResult(result.netOK, result.upstreamErr)
	select {
	case f := <-recorder.events:
		if errors.Is(cause, routing.ErrNoAvailableRelay) || f.Result == nil || *f.Result || !errors.Is(f.Err, cause) {
			t.Fatalf("lost deferred failure evidence: %+v", f)
		}
	case <-time.After(50 * time.Millisecond):
		if !errors.Is(cause, routing.ErrNoAvailableRelay) {
			t.Fatal("missing deferred failure feedback")
		}
	}
}

type deferredRelayConn struct {
	net.Conn
	ready <-chan struct{}
	err   error
}

func (c *deferredRelayConn) Read([]byte) (int, error) {
	<-c.ready
	return 0, c.err
}
