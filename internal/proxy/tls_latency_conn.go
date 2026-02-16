package proxy

import (
	"net"
	"sync/atomic"
	"time"
)

// tlsLatencyConn wraps a net.Conn to measure TLS handshake latency by
// detecting the Client Hello write and Server Hello read boundaries.
//
// State machine (atomic uint32):
//
//	0 = init (waiting for first write)
//	1 = handshake_started (Client Hello written, waiting for first read)
//	2 = done (Server Hello received, latency captured)
type tlsLatencyConn struct {
	net.Conn
	state     uint32 // atomic
	startTime int64  // atomic, UnixNano
	onLatency func(time.Duration)
}

func newTLSLatencyConn(c net.Conn, onLatency func(time.Duration)) *tlsLatencyConn {
	return &tlsLatencyConn{
		Conn:      c,
		onLatency: onLatency,
	}
}

func (c *tlsLatencyConn) Write(b []byte) (int, error) {
	// Fast path: handshake already started or done.
	if atomic.LoadUint32(&c.state) != 0 {
		return c.Conn.Write(b)
	}

	n, err := c.Conn.Write(b)

	// Capture first write (Client Hello) — start timer.
	if n > 0 && err == nil {
		if atomic.CompareAndSwapUint32(&c.state, 0, 1) {
			atomic.StoreInt64(&c.startTime, time.Now().UnixNano())
		}
	}
	return n, err
}

func (c *tlsLatencyConn) Read(b []byte) (int, error) {
	// Fast path: not in handshake phase.
	if atomic.LoadUint32(&c.state) != 1 {
		return c.Conn.Read(b)
	}

	n, err := c.Conn.Read(b)

	// Capture first read (Server Hello) — stop timer.
	if n > 0 && err == nil {
		if atomic.CompareAndSwapUint32(&c.state, 1, 2) {
			startNano := atomic.LoadInt64(&c.startTime)
			if startNano > 0 {
				latency := time.Duration(time.Now().UnixNano() - startNano)
				if c.onLatency != nil {
					go c.onLatency(latency)
				}
			}
		}
	}
	return n, err
}
