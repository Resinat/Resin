package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	M "github.com/sagernet/sing/common/metadata"
)

func TestShouldRecordForwardCopyFailure(t *testing.T) {
	t.Run("NilError", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com", nil)
		if shouldRecordForwardCopyFailure(req, nil) {
			t.Fatal("nil copy error should not be recorded as failure")
		}
	})

	t.Run("ClientCanceledContext", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest("GET", "http://example.com", nil).WithContext(ctx)
		if shouldRecordForwardCopyFailure(req, genericErr{}) {
			t.Fatal("client-canceled request should not penalize node health")
		}
	})

	t.Run("ContextCanceledError", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com", nil)
		if shouldRecordForwardCopyFailure(req, canceledErr{}) {
			t.Fatal("context canceled errors should not penalize node health")
		}
	})

	t.Run("UpstreamTimeout", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com", nil)
		if !shouldRecordForwardCopyFailure(req, deadlineExceededErr{}) {
			t.Fatal("upstream timeout should be recorded as node failure")
		}
	})

	t.Run("GenericUpstreamError", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com", nil)
		err := errors.New("upstream read failed")
		if !shouldRecordForwardCopyFailure(req, err) {
			t.Fatal("generic upstream read error should be recorded as node failure")
		}
	})
}

func TestForwardBodyFailureRetainsActualRelay(t *testing.T) {
	env := newProxyE2EEnv(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, "short")
	}))
	defer upstream.Close()
	info := node.RelayInfo{Hash: node.Hash{42}, Version: 7}
	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, dest.String())
		if err != nil {
			return nil, err
		}
		return &logTestRelayConn{Conn: conn, info: info}, nil
	})
	recorder := &feedbackCapture{events: make(chan node.Feedback, 8)}
	transports := newOutboundTransportPool()
	defer transports.CloseAll()
	fp := NewForwardProxy(ForwardProxyConfig{Router: env.router, Pool: env.pool, Health: recorder, TransportPool: transports})
	req := httptest.NewRequest(http.MethodGet, upstream.URL, nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", ""))
	w := httptest.NewRecorder()
	fp.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "short" {
		t.Fatalf("expected partially delivered response: status=%d body=%q", w.Code, w.Body.String())
	}
	deadline := time.After(time.Second)
	for {
		select {
		case f := <-recorder.events:
			if f.Result == nil {
				continue // latency-attempt feedback
			}
			var relay *node.RelayError
			if *f.Result || !errors.Is(f.Err, io.ErrUnexpectedEOF) || !errors.As(f.Err, &relay) || relay.ServerDial || relay.Hash != info.Hash || relay.Version != info.Version || f.Relay != info {
				t.Fatalf("body failure lost connection identity: %+v", f)
			}
			return
		case <-deadline:
			t.Fatal("missing response-body failure feedback")
		}
	}
}
