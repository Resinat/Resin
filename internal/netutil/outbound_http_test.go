package netutil

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

func TestHTTPGetViaOutbound_RequireStatusOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer srv.Close()

	ob, err := (&testutil.StubOutboundBuilder{}).Build(nil)
	if err != nil {
		t.Fatalf("build outbound: %v", err)
	}
	_, err = HTTPGetViaOutbound(context.Background(), ob, srv.URL, OutboundHTTPOptions{
		RequireStatusOK: true,
	})
	if err == nil {
		t.Fatal("expected non-200 status to return error")
	}
	if !strings.Contains(err.Error(), "unexpected status 404") {
		t.Fatalf("expected status error, got: %v", err)
	}
}

func TestHTTPGetViaOutbound_AllowNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("probe-body"))
	}))
	defer srv.Close()

	ob, err := (&testutil.StubOutboundBuilder{}).Build(nil)
	if err != nil {
		t.Fatalf("build outbound: %v", err)
	}
	result, err := HTTPGetViaOutbound(context.Background(), ob, srv.URL, OutboundHTTPOptions{
		RequireStatusOK: false,
	})
	if err != nil {
		t.Fatalf("expected non-200 response to pass through, got: %v", err)
	}
	if string(result.Body) != "probe-body" {
		t.Fatalf("unexpected body %q", string(result.Body))
	}
}

func TestHTTPGetViaOutbound_RedirectRelayIdentity(t *testing.T) {
	bindingA, bindingB := &outboundHTTPTestBinding{}, &outboundHTTPTestBinding{}
	relayA := node.RelayInfo{Hash: node.Hash{1}, Tag: "relay-a", Version: 11, Binding: bindingA}
	relayB := node.RelayInfo{Hash: node.Hash{2}, Tag: "relay-b", Version: 22, Binding: bindingB}
	dialErr := errors.New("redirect target dial failed")

	// DisableKeepAlives prevents traditional idle-connection retries. A real
	// redirect exercises the next GetConn, which must clear the previous relay
	// even when the new attempt fails before GotConn.
	for _, tc := range []struct {
		name        string
		secondRelay node.RelayInfo
		dialErr     error
	}{
		{name: "relay_b_certificate_failure", secondRelay: relayB},
		{name: "dial_failure", dialErr: dialErr},
		{name: "unmarked_connection_certificate_failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("failed TLS verification must not reach the target HTTP handler")
			}))
			target.Config.ErrorLog = log.New(io.Discard, "", 0)
			target.StartTLS()
			defer target.Close()

			var redirects atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirects.Add(1)
				http.Redirect(w, r, target.URL, http.StatusFound)
			}))
			defer first.Close()

			var dials atomic.Int32
			var dialer net.Dialer
			ob := &outboundHTTPTestOutbound{
				Outbound: testutil.NewNoopOutbound(),
				dialContext: func(ctx context.Context, network string, addr M.Socksaddr) (net.Conn, error) {
					attempt := dials.Add(1)
					if attempt > 2 {
						return nil, fmt.Errorf("unexpected dial attempt %d", attempt)
					}
					info, wantAddr := relayA, first.Listener.Addr().String()
					if attempt == 2 {
						info, wantAddr = tc.secondRelay, target.Listener.Addr().String()
					}
					if addr.String() != wantAddr {
						return nil, fmt.Errorf("dial %d destination = %s, want %s", attempt, addr, wantAddr)
					}
					if attempt == 2 && tc.dialErr != nil {
						return nil, tc.dialErr
					}
					conn, err := dialer.DialContext(ctx, network, addr.String())
					if err != nil {
						return nil, err
					}
					if info == (node.RelayInfo{}) {
						return conn, nil
					}
					return &outboundHTTPTestMarkedConn{Conn: conn, info: info}, nil
				},
			}

			var getConns, gotConns atomic.Int32
			var firstRelay atomic.Pointer[node.RelayInfo]
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
				GetConn: func(string) { getConns.Add(1) },
				GotConn: func(conn httptrace.GotConnInfo) {
					info := node.RelayInfoFromConn(conn.Conn)
					firstRelay.Store(&info)
					gotConns.Add(1)
				},
			})
			result, err := HTTPGetViaOutbound(ctx, ob, first.URL, OutboundHTTPOptions{
				OnConnLifecycle: func(ConnLifecycleOp) {},
			})
			if err == nil {
				t.Fatal("expected the redirected request to fail")
			}
			if redirects.Load() != 1 || dials.Load() != 2 || getConns.Load() != 2 || gotConns.Load() != 1 {
				t.Fatalf("want one HTTP response and a failure before the second GotConn: redirects=%d dials=%d GetConn=%d GotConn=%d",
					redirects.Load(), dials.Load(), getConns.Load(), gotConns.Load())
			}
			if got := firstRelay.Load(); got == nil || *got != relayA {
				t.Fatalf("first response relay = %+v, want %+v", got, relayA)
			}
			if tc.dialErr != nil {
				if !errors.Is(err, tc.dialErr) {
					t.Fatalf("lost redirect dial failure: %v", err)
				}
			} else {
				var certificate x509.UnknownAuthorityError
				if !errors.As(err, &certificate) {
					t.Fatalf("lost target certificate failure: %v", err)
				}
			}
			if result.Relay != tc.secondRelay {
				t.Fatalf("redirect result relay = %+v, want %+v", result.Relay, tc.secondRelay)
			}
			if got := node.BindingFromError(err); got != tc.secondRelay.Binding {
				t.Fatalf("redirect error binding = %v, want %v: %v", got, tc.secondRelay.Binding, err)
			}
			var evidence *node.RelayError
			if tc.secondRelay.Hash.IsZero() {
				if errors.As(err, &evidence) {
					t.Fatalf("redirect inherited relay evidence from the first response: %+v", evidence)
				}
			} else if !errors.As(err, &evidence) || evidence.Hash != relayB.Hash || evidence.Version != relayB.Version || evidence.ServerDial {
				t.Fatalf("target TLS failure lost relay B attribution: %v", err)
			}
		})
	}
}

type outboundHTTPTestOutbound struct {
	adapter.Outbound
	dialContext func(context.Context, string, M.Socksaddr) (net.Conn, error)
}

func (o *outboundHTTPTestOutbound) DialContext(ctx context.Context, network string, addr M.Socksaddr) (net.Conn, error) {
	return o.dialContext(ctx, network, addr)
}

type outboundHTTPTestMarkedConn struct {
	net.Conn
	info node.RelayInfo
}

func (c *outboundHTTPTestMarkedConn) RelayInfo() node.RelayInfo { return c.info }

type outboundHTTPTestBinding struct{ invalidated atomic.Bool }

func (b *outboundHTTPTestBinding) Invalidate() { b.invalidated.Store(true) }

func TestConnCloseHook_CloseIsIdempotentAndConcurrentSafe(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	var onCloseCount atomic.Int32
	hook := &connCloseHook{
		Conn: client,
		onClose: func() {
			onCloseCount.Add(1)
		},
	}

	const closers = 32
	var wg sync.WaitGroup
	wg.Add(closers)
	for i := 0; i < closers; i++ {
		go func() {
			defer wg.Done()
			_ = hook.Close()
		}()
	}
	wg.Wait()

	if got := onCloseCount.Load(); got != 1 {
		t.Fatalf("onClose called %d times, want 1", got)
	}
}
