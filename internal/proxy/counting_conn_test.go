package proxy

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type trafficDelta struct {
	ingress int64
	egress  int64
}

type countingConnTestSink struct {
	traffic chan trafficDelta
	connOps chan ConnectionOp
}

func newCountingConnTestSink() *countingConnTestSink {
	return &countingConnTestSink{
		traffic: make(chan trafficDelta, 16),
		connOps: make(chan ConnectionOp, 16),
	}
}

func (s *countingConnTestSink) OnTrafficDelta(ingressBytes, egressBytes int64) {
	s.traffic <- trafficDelta{ingress: ingressBytes, egress: egressBytes}
}

func (s *countingConnTestSink) OnConnectionLifecycle(direction ConnectionDirection, op ConnectionOp) {
	if direction == ConnectionOutbound {
		s.connOps <- op
	}
}

type stubConn struct {
	closed atomic.Bool
}

func (c *stubConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (c *stubConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *stubConn) LocalAddr() net.Addr                { return stubAddr("local") }
func (c *stubConn) RemoteAddr() net.Addr               { return stubAddr("remote") }
func (c *stubConn) SetDeadline(_ time.Time) error      { return nil }
func (c *stubConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *stubConn) SetWriteDeadline(_ time.Time) error { return nil }
func (c *stubConn) Close() error {
	c.closed.Store(true)
	return nil
}

type stubAddr string

func (a stubAddr) Network() string { return "tcp" }
func (a stubAddr) String() string  { return string(a) }

func waitTrafficDelta(t *testing.T, ch <-chan trafficDelta, timeout time.Duration) trafficDelta {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(timeout):
		t.Fatalf("expected traffic delta within %s", timeout)
		return trafficDelta{}
	}
}

func expectNoTrafficDelta(t *testing.T, ch <-chan trafficDelta, timeout time.Duration) {
	t.Helper()
	select {
	case d := <-ch:
		t.Fatalf("unexpected extra traffic delta: %+v", d)
	case <-time.After(timeout):
	}
}

func TestCountingConn_DeferredFlushReportsSmallTraffic(t *testing.T) {
	prev := trafficFlushInterval
	trafficFlushInterval = 20 * time.Millisecond
	t.Cleanup(func() { trafficFlushInterval = prev })

	sink := newCountingConnTestSink()
	conn := newCountingConn(&stubConn{}, sink)
	defer conn.Close()

	if _, err := conn.Write(make([]byte, 128)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := waitTrafficDelta(t, sink.traffic, 300*time.Millisecond)
	if got.ingress != 0 || got.egress != 128 {
		t.Fatalf("traffic delta mismatch: got %+v, want ingress=0 egress=128", got)
	}
	expectNoTrafficDelta(t, sink.traffic, 60*time.Millisecond)
}

func TestCountingConn_ThresholdFlushIsImmediate(t *testing.T) {
	prev := trafficFlushInterval
	trafficFlushInterval = 2 * time.Second
	t.Cleanup(func() { trafficFlushInterval = prev })

	sink := newCountingConnTestSink()
	conn := newCountingConn(&stubConn{}, sink)
	defer conn.Close()

	start := time.Now()
	if _, err := conn.Write(make([]byte, trafficFlushThreshold)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := waitTrafficDelta(t, sink.traffic, 120*time.Millisecond)
	if got.ingress != 0 || got.egress != trafficFlushThreshold {
		t.Fatalf(
			"traffic delta mismatch: got %+v, want ingress=0 egress=%d",
			got,
			trafficFlushThreshold,
		)
	}
	if elapsed := time.Since(start); elapsed >= trafficFlushInterval {
		t.Fatalf("threshold flush waited for deferred interval: elapsed=%s interval=%s", elapsed, trafficFlushInterval)
	}
	expectNoTrafficDelta(t, sink.traffic, 50*time.Millisecond)
}

func TestCountingConn_CloseFlushesPendingOnce(t *testing.T) {
	prev := trafficFlushInterval
	trafficFlushInterval = 50 * time.Millisecond
	t.Cleanup(func() { trafficFlushInterval = prev })

	sink := newCountingConnTestSink()
	conn := newCountingConn(&stubConn{}, sink)

	if _, err := conn.Write(make([]byte, 77)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := waitTrafficDelta(t, sink.traffic, 100*time.Millisecond)
	if got.ingress != 0 || got.egress != 77 {
		t.Fatalf("traffic delta mismatch: got %+v, want ingress=0 egress=77", got)
	}
	select {
	case op := <-sink.connOps:
		if op != ConnectionClose {
			t.Fatalf("unexpected connection op: got %v, want %v", op, ConnectionClose)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected outbound close event")
	}
	expectNoTrafficDelta(t, sink.traffic, 90*time.Millisecond)
}
