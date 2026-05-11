package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/proxy"
)

type stubSocksHandler struct {
	firstByteCh chan byte
}

type demuxHalfCloseConn struct {
	net.Conn
	closeWriteCalls int
	closeReadCalls  int
}

func (c *demuxHalfCloseConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (c *demuxHalfCloseConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *demuxHalfCloseConn) LocalAddr() net.Addr                { return stubAddr("local") }
func (c *demuxHalfCloseConn) RemoteAddr() net.Addr               { return stubAddr("remote") }
func (c *demuxHalfCloseConn) SetDeadline(_ time.Time) error      { return nil }
func (c *demuxHalfCloseConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *demuxHalfCloseConn) SetWriteDeadline(_ time.Time) error { return nil }
func (c *demuxHalfCloseConn) Close() error                       { return nil }
func (c *demuxHalfCloseConn) CloseWrite() error {
	c.closeWriteCalls++
	return nil
}

func (c *demuxHalfCloseConn) CloseRead() error {
	c.closeReadCalls++
	return nil
}

type stubAddr string

func (a stubAddr) Network() string { return "tcp" }
func (a stubAddr) String() string  { return string(a) }

type temporaryNetError struct {
	err error
}

func (e temporaryNetError) Error() string {
	if e.err == nil {
		return "temporary network error"
	}
	return e.err.Error()
}

func (e temporaryNetError) Timeout() bool   { return false }
func (e temporaryNetError) Temporary() bool { return true }

type temporaryErrorListener struct {
	net.Listener
	mu        sync.Mutex
	issued    bool
	acceptErr error
}

func (l *temporaryErrorListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.issued {
		l.issued = true
		err := l.acceptErr
		l.mu.Unlock()
		return nil, temporaryNetError{err: err}
	}
	l.mu.Unlock()
	return l.Listener.Accept()
}

func (h *stubSocksHandler) ServeConnContext(_ context.Context, conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	b, err := reader.ReadByte()
	if err != nil {
		return
	}
	if h.firstByteCh != nil {
		h.firstByteCh <- b
	}
	_, _ = conn.Write([]byte{0x05, 0x00})
}

func waitForDemuxConnState(t *testing.T, demux *inboundDemuxServer, wantActive int, wantSniff int) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		demux.mu.Lock()
		active := len(demux.activeConns)
		sniff := len(demux.sniffConns)
		demux.mu.Unlock()
		if active == wantActive && sniff == wantSniff {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for demux conn state active=%d sniff=%d", wantActive, wantSniff)
}

func TestInboundDemux_RoutesHTTPToHTTPServer(t *testing.T) {
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Demux-Route", "http")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}),
	}
	socksHandler := &stubSocksHandler{firstByteCh: make(chan byte, 1)}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	demux := newInboundDemuxServer(httpServer, socksHandler)
	errCh := make(chan error, 1)
	go func() {
		errCh <- demux.Serve(ln)
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer clientConn.Close()

	if _, err := io.WriteString(clientConn, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write http request: %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(clientConn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read http response: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if resp.Header.Get("X-Demux-Route") != "http" {
		t.Fatalf("X-Demux-Route: got %q, want %q", resp.Header.Get("X-Demux-Route"), "http")
	}

	select {
	case b := <-socksHandler.firstByteCh:
		t.Fatalf("unexpected SOCKS handler call with first byte %d", b)
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := demux.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for demux server to stop")
	}
}

func TestInboundDemux_RetriesTemporaryAcceptError(t *testing.T) {
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Demux-Route", "http")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}),
	}

	baseLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := &temporaryErrorListener{
		Listener:  baseLn,
		acceptErr: errors.New("temporary accept failure"),
	}

	demux := newInboundDemuxServer(httpServer, &stubSocksHandler{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- demux.Serve(ln)
	}()

	time.Sleep(20 * time.Millisecond)
	select {
	case err := <-errCh:
		t.Fatalf("serve returned after temporary accept error: %v", err)
	default:
	}

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer clientConn.Close()

	if _, err := io.WriteString(clientConn, "GET /retry HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write http request: %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(clientConn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read http response: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if resp.Header.Get("X-Demux-Route") != "http" {
		t.Fatalf("X-Demux-Route: got %q, want %q", resp.Header.Get("X-Demux-Route"), "http")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := demux.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for demux server to stop")
	}
}

func TestInboundDemux_IdleConnectionDoesNotBlockSubsequentAccepts(t *testing.T) {
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Demux-Route", "http")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	demux := newInboundDemuxServer(httpServer, &stubSocksHandler{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- demux.Serve(ln)
	}()

	idleConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial idle conn: %v", err)
	}
	defer idleConn.Close()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial http conn: %v", err)
	}
	defer clientConn.Close()

	if _, err := io.WriteString(clientConn, "GET /after-idle HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write http request: %v", err)
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read http response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if resp.Header.Get("X-Demux-Route") != "http" {
		t.Fatalf("X-Demux-Route: got %q, want %q", resp.Header.Get("X-Demux-Route"), "http")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := demux.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for demux server to stop")
	}
}

func TestInboundDemux_ShutdownClosesIdleSniffConnectionsPromptly(t *testing.T) {
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	demux := newInboundDemuxServer(httpServer, &stubSocksHandler{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- demux.Serve(ln)
	}()

	idleConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial idle conn: %v", err)
	}
	defer idleConn.Close()

	waitForDemuxConnState(t, demux, 1, 1)

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- demux.Shutdown(ctx)
	}()

	_ = idleConn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	buf := make([]byte, 1)
	if _, err := idleConn.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("idle sniff conn should be closed during shutdown, got %v", err)
	}

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish promptly")
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for demux server to stop")
	}
}

func TestInboundDemux_ShutdownUnblocksSocksHandshakePhase(t *testing.T) {
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected HTTP handler invocation for partial SOCKS5 handshake")
		}),
	}

	socksHandler := proxy.NewSocks5Inbound(proxy.Socks5InboundConfig{
		AuthVersion: string(config.AuthVersionV1),
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	demux := newInboundDemuxServer(httpServer, socksHandler)
	errCh := make(chan error, 1)
	go func() {
		errCh <- demux.Serve(ln)
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial socks conn: %v", err)
	}
	defer clientConn.Close()

	if _, err := clientConn.Write([]byte{0x05, 0x01}); err != nil {
		t.Fatalf("write partial socks handshake: %v", err)
	}

	waitForDemuxConnState(t, demux, 1, 0)

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- demux.Shutdown(ctx)
	}()

	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := clientConn.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("partial socks handshake conn should be closed during shutdown, got %v", err)
	}

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish promptly for partial socks handshake")
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for demux server to stop")
	}
}

func TestInboundDemux_ShutdownTimeoutForceClosesActiveHTTPConnections(t *testing.T) {
	handlerStarted := make(chan struct{})
	handlerRelease := make(chan struct{})
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(handlerStarted)
			select {
			case <-handlerRelease:
			case <-r.Context().Done():
			}
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	demux := newInboundDemuxServer(httpServer, &stubSocksHandler{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- demux.Serve(ln)
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial http conn: %v", err)
	}
	defer clientConn.Close()
	defer close(handlerRelease)

	if _, err := io.WriteString(clientConn, "GET /hang HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatalf("write hanging http request: %v", err)
	}

	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("expected HTTP handler to start")
	}

	waitForDemuxConnState(t, demux, 1, 0)

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		shutdownDone <- demux.Shutdown(ctx)
	}()

	select {
	case err := <-shutdownDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown: got %v, want %v", err, context.DeadlineExceeded)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not return after timeout")
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := clientConn.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("active HTTP conn should be force-closed after shutdown timeout, got %v", err)
	}

	waitForDemuxConnState(t, demux, 0, 0)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for demux server to stop")
	}
}

func TestInboundDemux_ShutdownTimeoutForceClosesActiveHTTPConnection(t *testing.T) {
	handlerStarted := make(chan struct{})
	handlerDone := make(chan struct{})
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(handlerStarted)
			<-r.Context().Done()
			close(handlerDone)
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	demux := newInboundDemuxServer(httpServer, &stubSocksHandler{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- demux.Serve(ln)
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial http conn: %v", err)
	}
	defer clientConn.Close()

	if _, err := io.WriteString(clientConn, "GET /hang HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatalf("write http request: %v", err)
	}

	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	waitForDemuxConnState(t, demux, 1, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = demux.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error: got %v, want context deadline exceeded", err)
	}

	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("active HTTP handler was not interrupted after forced shutdown")
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := clientConn.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("active HTTP conn should be closed during forced shutdown, got %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for demux server to stop")
	}
}

func TestPrebufferedConn_ForwardsHalfClose(t *testing.T) {
	base := &demuxHalfCloseConn{}
	reader := bufio.NewReader(strings.NewReader("prefetched"))
	if _, err := reader.Peek(len("prefetched")); err != nil {
		t.Fatalf("Peek: %v", err)
	}

	conn, err := newPrebufferedConn(base, reader)
	if err != nil {
		t.Fatalf("newPrebufferedConn: %v", err)
	}
	if _, ok := conn.(*prebufferedConn); !ok {
		t.Fatal("expected wrapped prebufferedConn")
	}

	closeWriter, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("prebufferedConn should preserve CloseWrite")
	}
	closeReader, ok := conn.(interface{ CloseRead() error })
	if !ok {
		t.Fatal("prebufferedConn should preserve CloseRead")
	}

	if err := closeWriter.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	if err := closeReader.CloseRead(); err != nil {
		t.Fatalf("CloseRead: %v", err)
	}
	if base.closeWriteCalls != 1 {
		t.Fatalf("CloseWrite calls: got %d, want 1", base.closeWriteCalls)
	}
	if base.closeReadCalls != 1 {
		t.Fatalf("CloseRead calls: got %d, want 1", base.closeReadCalls)
	}
}

func TestPrebufferedConn_CloseWriteReportsUnsupportedUnderlying(t *testing.T) {
	base, peer := net.Pipe()
	defer base.Close()
	defer peer.Close()

	reader := bufio.NewReader(strings.NewReader("prefetched"))
	if _, err := reader.Peek(len("prefetched")); err != nil {
		t.Fatalf("Peek: %v", err)
	}
	conn, err := newPrebufferedConn(base, reader)
	if err != nil {
		t.Fatalf("newPrebufferedConn: %v", err)
	}
	if _, ok := conn.(*prebufferedConn); !ok {
		t.Fatal("expected wrapped prebufferedConn")
	}

	closeWriter, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("prebufferedConn should expose CloseWrite")
	}
	if err := closeWriter.CloseWrite(); err == nil {
		t.Fatal("CloseWrite should report unsupported when underlying conn cannot half-close")
	}
}

func TestInboundDemux_RoutesSocks5ByFirstByte(t *testing.T) {
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected HTTP handler invocation for SOCKS5 bytes")
		}),
	}
	socksHandler := &stubSocksHandler{firstByteCh: make(chan byte, 1)}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	demux := newInboundDemuxServer(httpServer, socksHandler)
	errCh := make(chan error, 1)
	go func() {
		errCh <- demux.Serve(ln)
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer clientConn.Close()

	if _, err := clientConn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write socks greeting: %v", err)
	}

	reply := make([]byte, 2)
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("read socks reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("reply: got %v, want [5 0]", reply)
	}

	select {
	case b := <-socksHandler.firstByteCh:
		if b != 0x05 {
			t.Fatalf("first byte: got %d, want %d", b, 0x05)
		}
	case <-time.After(time.Second):
		t.Fatal("expected SOCKS handler to be called")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := demux.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for demux server to stop")
	}
}

func TestInboundDemux_PreservesHTTPFirstByteAfterPeek(t *testing.T) {
	requestLineCh := make(chan string, 1)
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestLineCh <- r.Method + " " + r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	demux := newInboundDemuxServer(httpServer, &stubSocksHandler{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- demux.Serve(ln)
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer clientConn.Close()

	rawReq := "GET /peek HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(clientConn, rawReq); err != nil {
		t.Fatalf("write request: %v", err)
	}

	respBytes, err := io.ReadAll(clientConn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !strings.Contains(string(respBytes), "204 No Content") {
		t.Fatalf("unexpected response: %q", string(respBytes))
	}

	select {
	case requestLine := <-requestLineCh:
		if requestLine != "GET /peek" {
			t.Fatalf("requestLine: got %q, want %q", requestLine, "GET /peek")
		}
	case <-time.After(time.Second):
		t.Fatal("expected HTTP handler to receive request")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := demux.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for demux server to stop")
	}
}

func TestConnChannelListener_CloseRejectsQueuedConnAndFutureEnqueue(t *testing.T) {
	listener := newConnChannelListener()
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	secondClientConn, secondServerConn := net.Pipe()
	defer secondClientConn.Close()
	defer secondServerConn.Close()

	if err := listener.Enqueue(serverConn); err != nil {
		t.Fatalf("enqueue before close: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("accept after close: got %v, want %v", err, net.ErrClosed)
	}
	if err := listener.Enqueue(secondServerConn); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("enqueue after close: got %v, want %v", err, net.ErrClosed)
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := clientConn.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("queued conn should be closed on listener close, got %v", err)
	}
}

func TestInboundDemux_TryStartConnWorkerStopsOnceShutdownBegins(t *testing.T) {
	demux := &inboundDemuxServer{}
	if !demux.tryStartConnWorker() {
		t.Fatal("expected initial worker registration to succeed")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- demux.Shutdown(ctx)
	}()

	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before in-flight worker finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	if demux.tryStartConnWorker() {
		t.Fatal("should reject new worker registration after shutdown begins")
	}

	demux.workerWG.Done()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shutdown after worker completion")
	}
}
