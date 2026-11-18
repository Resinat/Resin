package proxy

import (
	"context"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	M "github.com/sagernet/sing/common/metadata"
)

type tunnelTestBinding struct{ invalidations atomic.Int32 }

func (b *tunnelTestBinding) Invalidate() { b.invalidations.Add(1) }

func awaitTunnelResultFeedback(t *testing.T, recorder *feedbackCapture) node.Feedback {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case f := <-recorder.events:
			if f.Result != nil {
				return f
			}
		case <-deadline:
			t.Fatal("missing tunnel result feedback")
		}
	}
}

func TestPreparedTunnelZeroTrafficFailureRetainsBinding(t *testing.T) {
	for _, name := range []string{"binding_only", "known_relay"} {
		t.Run(name, func(t *testing.T) {
			env := newProxyE2EEnv(t)
			binding := new(tunnelTestBinding)
			info := node.RelayInfo{Binding: binding}
			if name == "known_relay" {
				info.Hash, info.Version = node.Hash{42}, 7
			}
			setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
				conn, peer := net.Pipe()
				_ = peer.Close()
				return &logTestRelayConn{Conn: conn, info: info}, nil
			})
			recorder := &feedbackCapture{events: make(chan node.Feedback, 8)}
			// CONNECT and SOCKS share the prepared session's recordResult callback.
			prepared := prepareConnectTunnel(context.Background(), tunnelDeps{router: env.router, pool: env.pool, health: recorder}, "plat", "", "example.com:443")
			if prepared.session == nil {
				t.Fatalf("prepare failed: %+v", prepared)
			}
			defer prepared.session.upstreamConn.Close()

			// Another request opens the landing circuit after this connection was made.
			for range 3 {
				env.pool.RecordResult(prepared.route.NodeHash, false)
			}
			entry, _ := env.pool.GetEntry(prepared.route.NodeHash)
			if !entry.IsCircuitOpen() {
				t.Fatal("landing circuit must be open before recording the tunnel result")
			}

			client, peer := net.Pipe()
			defer client.Close()
			defer peer.Close()
			result := pumpPreparedTunnelReader(client, strings.NewReader(""), prepared.session, tunnelPumpOptions{requireBidirectionalTraffic: true})
			if result.netOK || result.upstreamErr != nil || result.upstreamStage != "connect_zero_traffic" || result.ingressBytes != 0 || result.egressBytes != 0 {
				t.Fatalf("expected zero-traffic failure without a network error: %+v", result)
			}
			prepared.session.recordResult(result.netOK, result.upstreamErr)
			f := awaitTunnelResultFeedback(t, recorder)
			if *f.Result || f.Err != nil || f.Relay != info {
				t.Fatalf("nil-error failure lost connection binding or invented an error: %+v", f)
			}
			if !env.pool.ApplyFeedback(prepared.route.NodeHash, prepared.route.NodeVersion, f) {
				t.Fatal("pool rejected tunnel failure feedback")
			}
			if got := binding.invalidations.Load(); got != 1 {
				t.Fatalf("binding invalidations: got %d, want 1", got)
			}
		})
	}
}

func TestPrepareTunnelDialFailurePreservesBindingEvidence(t *testing.T) {
	env := newProxyE2EEnv(t)
	binding := new(tunnelTestBinding)
	info := node.RelayInfo{Hash: node.Hash{42}, Version: 7, Binding: binding}
	cause := &node.RelayError{Hash: info.Hash, Version: info.Version, ServerDial: true, Err: io.ErrUnexpectedEOF}
	dialErr := info.WrapError(cause)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return nil, dialErr
	})
	recorder := &feedbackCapture{events: make(chan node.Feedback, 8)}
	prepared := prepareConnectTunnel(context.Background(), tunnelDeps{router: env.router, pool: env.pool, health: recorder}, "plat", "", "example.com:443")
	if prepared.session != nil || prepared.upstreamErr != dialErr {
		t.Fatalf("expected immediate dial failure: %+v", prepared)
	}
	f := awaitTunnelResultFeedback(t, recorder)
	if *f.Result || f.Err != dialErr || f.Relay != (node.RelayInfo{}) || node.BindingFromError(f.Err) != binding {
		t.Fatalf("dial failure replaced lower-level evidence with connection metadata: %+v", f)
	}
}

func TestPumpPreparedTunnelReader_FallsBackToFullCloseWhenHalfCloseUnavailable(t *testing.T) {
	clientBase, clientPeer := net.Pipe()
	upstreamBase, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()

	clientConn := &connCloseNotifier{
		Conn: clientBase,
		sink: newCountingConnTestSink(),
	}
	upstreamConn := newTLSLatencyConn(newCountingConn(upstreamBase, newCountingConnTestSink()), nil)

	clientPayloadDone := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(clientPeer)
		clientPayloadDone <- data
	}()

	done := make(chan struct{})
	go func() {
		_ = pumpPreparedTunnelReader(
			clientConn,
			clientConn,
			&preparedTunnel{
				upstreamConn: upstreamConn,
				recordResult: func(bool, error) {},
			},
			tunnelPumpOptions{},
		)
		close(done)
	}()

	go func() {
		_, _ = upstreamPeer.Write([]byte("server-push"))
		_ = upstreamPeer.Close()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pumpPreparedTunnelReader should fall back to full close when CloseWrite is unavailable")
	}

	select {
	case payload := <-clientPayloadDone:
		if string(payload) != "server-push" {
			t.Fatalf("client payload: got %q, want %q", string(payload), "server-push")
		}
	case <-time.After(time.Second):
		t.Fatal("expected client peer to receive upstream payload and EOF")
	}
}

func TestPumpPreparedTunnelReader_ClientReadResetAfterIngressDoesNotFail(t *testing.T) {
	clientLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen client side: %v", err)
	}
	defer clientLn.Close()

	clientAccepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := clientLn.Accept()
		if acceptErr != nil {
			clientAccepted <- nil
			return
		}
		clientAccepted <- conn
	}()

	clientConn, err := net.Dial("tcp", clientLn.Addr().String())
	if err != nil {
		t.Fatalf("dial client side: %v", err)
	}
	clientTCP, ok := clientConn.(*net.TCPConn)
	if !ok {
		t.Fatalf("client conn type: got %T, want *net.TCPConn", clientConn)
	}
	defer clientTCP.Close()

	proxyClientConn := <-clientAccepted
	if proxyClientConn == nil {
		t.Fatal("accept client side failed")
	}
	defer proxyClientConn.Close()

	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream side: %v", err)
	}
	defer upstreamLn.Close()

	upstreamAccepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := upstreamLn.Accept()
		if acceptErr != nil {
			upstreamAccepted <- nil
			return
		}
		upstreamAccepted <- conn
	}()

	upstreamPeer, err := net.Dial("tcp", upstreamLn.Addr().String())
	if err != nil {
		t.Fatalf("dial upstream side: %v", err)
	}
	defer upstreamPeer.Close()

	proxyUpstreamConn := <-upstreamAccepted
	if proxyUpstreamConn == nil {
		t.Fatal("accept upstream side failed")
	}
	defer proxyUpstreamConn.Close()

	resultCh := make(chan tunnelRelayResult, 1)
	go func() {
		resultCh <- pumpPreparedTunnelReader(
			proxyClientConn,
			proxyClientConn,
			&preparedTunnel{
				upstreamConn: proxyUpstreamConn,
				recordResult: func(bool, error) {},
			},
			tunnelPumpOptions{},
		)
	}()

	const request = "client-hello"
	const response = "server-reply"
	upstreamDone := make(chan error, 1)
	go func() {
		defer close(upstreamDone)
		buf := make([]byte, len(request))
		if _, err := io.ReadFull(upstreamPeer, buf); err != nil {
			upstreamDone <- err
			return
		}
		if string(buf) != request {
			upstreamDone <- io.ErrUnexpectedEOF
			return
		}
		if _, err := upstreamPeer.Write([]byte(response)); err != nil {
			upstreamDone <- err
			return
		}
		_, _ = io.Copy(io.Discard, upstreamPeer)
		upstreamDone <- nil
	}()

	if _, err := clientTCP.Write([]byte(request)); err != nil {
		t.Fatalf("write client request: %v", err)
	}

	respBuf := make([]byte, len(response))
	if _, err := io.ReadFull(clientTCP, respBuf); err != nil {
		t.Fatalf("read client response: %v", err)
	}
	if string(respBuf) != response {
		t.Fatalf("response: got %q, want %q", string(respBuf), response)
	}

	if err := clientTCP.SetLinger(0); err != nil {
		t.Fatalf("set linger: %v", err)
	}
	if err := clientTCP.Close(); err != nil {
		t.Fatalf("close client with reset: %v", err)
	}

	select {
	case result := <-resultCh:
		if !result.netOK {
			t.Fatalf("netOK: got false, want true (stage=%q err=%v)", result.upstreamStage, result.upstreamErr)
		}
		if result.proxyErr != nil {
			t.Fatalf("proxyErr: got %+v, want nil", result.proxyErr)
		}
		if result.upstreamStage != "" {
			t.Fatalf("upstreamStage: got %q, want empty", result.upstreamStage)
		}
		if result.ingressBytes != int64(len(response)) {
			t.Fatalf("ingressBytes: got %d, want %d", result.ingressBytes, len(response))
		}
		if result.egressBytes != int64(len(request)) {
			t.Fatalf("egressBytes: got %d, want %d", result.egressBytes, len(request))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected tunnel relay result")
	}

	select {
	case err := <-upstreamDone:
		if err != nil {
			t.Fatalf("upstream side failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected upstream side to finish")
	}
}
