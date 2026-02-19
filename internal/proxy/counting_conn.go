package proxy

import (
	"net"
	"sync/atomic"
)

// trafficFlushThreshold is the byte count at which a countingConn emits
// a traffic delta mid-stream. This ensures realtime throughput sampling
// and bucket aggregation see traffic during long-lived connections, not only
// at close. Fixed constant — not configurable.
const trafficFlushThreshold int64 = 32768 // 32 KB

// MetricsEventSink receives traffic and connection lifecycle events from the
// proxy layer. Implemented by metrics.Manager (wired in main.go).
// This interface is defined here (in the proxy package) to avoid an import
// cycle between proxy and metrics.
type MetricsEventSink interface {
	// OnTrafficDelta reports a traffic byte count delta for a platform.
	OnTrafficDelta(platformID string, ingressBytes, egressBytes int64)
	// OnConnectionLifecycle reports a connection open/close event.
	OnConnectionLifecycle(direction ConnectionDirection, op ConnectionOp)
}

// countingConn wraps a net.Conn, counting bytes read/written.
// Flushes a traffic delta every trafficFlushThreshold bytes
// and on Close (for the remainder).
type countingConn struct {
	net.Conn
	sink       MetricsEventSink
	platformID string

	pendingRead  atomic.Int64
	pendingWrite atomic.Int64
	closed       atomic.Bool
}

func newCountingConn(conn net.Conn, sink MetricsEventSink, platformID string) *countingConn {
	return &countingConn{
		Conn:       conn,
		sink:       sink,
		platformID: platformID,
	}
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		total := c.pendingRead.Add(int64(n))
		if total >= trafficFlushThreshold {
			flushed := c.pendingRead.Swap(0)
			if flushed > 0 {
				c.sink.OnTrafficDelta(c.platformID, flushed, 0)
			}
		}
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		total := c.pendingWrite.Add(int64(n))
		if total >= trafficFlushThreshold {
			flushed := c.pendingWrite.Swap(0)
			if flushed > 0 {
				c.sink.OnTrafficDelta(c.platformID, 0, flushed)
			}
		}
	}
	return n, err
}

func (c *countingConn) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil // already closed — idempotent
	}
	// Flush remaining bytes.
	pendR := c.pendingRead.Swap(0)
	pendW := c.pendingWrite.Swap(0)
	if pendR > 0 || pendW > 0 {
		c.sink.OnTrafficDelta(c.platformID, pendR, pendW)
	}
	c.sink.OnConnectionLifecycle(ConnectionOutbound, ConnectionClose)
	return c.Conn.Close()
}

// countingListener wraps a net.Listener, emitting connection lifecycle events
// on Accept (open) and on each connection's Close.
type countingListener struct {
	net.Listener
	sink MetricsEventSink
}

// NewCountingListener wraps a listener with connection lifecycle tracking.
func NewCountingListener(ln net.Listener, sink MetricsEventSink) net.Listener {
	if sink == nil {
		return ln
	}
	return &countingListener{Listener: ln, sink: sink}
}

func (cl *countingListener) Accept() (net.Conn, error) {
	conn, err := cl.Listener.Accept()
	if err != nil {
		return nil, err
	}
	cl.sink.OnConnectionLifecycle(ConnectionInbound, ConnectionOpen)
	return &connCloseNotifier{Conn: conn, sink: cl.sink}, nil
}

// connCloseNotifier emits a connection close event on Close.
type connCloseNotifier struct {
	net.Conn
	sink   MetricsEventSink
	closed atomic.Bool
}

func (c *connCloseNotifier) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		c.sink.OnConnectionLifecycle(ConnectionInbound, ConnectionClose)
	}
	return c.Conn.Close()
}
