package netutil

import (
	"context"
	"net"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
)

func TestWithoutHTTPTraceIsolatesNetworkHooks(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var outer, inner atomic.Int32
	parent := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { outer.Add(1) },
		DNSDone:      func(httptrace.DNSDoneInfo) { outer.Add(1) },
		ConnectStart: func(string, string) { outer.Add(1) },
		ConnectDone:  func(string, string, error) { outer.Add(1) },
	})
	dial := func(ctx context.Context) {
		t.Helper()
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
	isolated := WithoutHTTPTrace(parent)
	if httptrace.ContextClientTrace(isolated) != nil {
		t.Fatal("outer HTTP trace remained visible")
	}
	dial(isolated)
	dial(httptrace.WithClientTrace(isolated, &httptrace.ClientTrace{
		ConnectStart: func(string, string) { inner.Add(1) },
	}))
	if outer.Load() != 0 || inner.Load() != 1 {
		t.Fatalf("network hooks leaked: outer=%d inner=%d", outer.Load(), inner.Load())
	}
	dial(parent)
	if outer.Load() != 2 {
		t.Fatal("isolating the child changed the parent's network hooks")
	}
}
