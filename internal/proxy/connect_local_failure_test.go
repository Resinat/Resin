package proxy

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

var errForcedWriteFailure = errors.New("forced write failure")

type failOnWriteConn struct {
	net.Conn
	failAt int
	writes int
}

func (c *failOnWriteConn) Write(p []byte) (int, error) {
	c.writes++
	if c.failAt > 0 && c.writes == c.failAt {
		return 0, errForcedWriteFailure
	}
	return c.Conn.Write(p)
}

type hijackTestResponseWriter struct {
	header http.Header
	conn   net.Conn
	rw     *bufio.ReadWriter
}

func (w *hijackTestResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *hijackTestResponseWriter) Write(p []byte) (int, error) {
	if w.rw == nil || w.rw.Writer == nil {
		return 0, net.ErrClosed
	}
	return w.rw.Write(p)
}

func (w *hijackTestResponseWriter) WriteHeader(statusCode int) {
	w.Header().Set("X-Test-Status", http.StatusText(statusCode))
}

func (w *hijackTestResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.conn == nil || w.rw == nil {
		return nil, nil, net.ErrClosed
	}
	return w.conn, w.rw, nil
}
