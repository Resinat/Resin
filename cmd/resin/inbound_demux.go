package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

var errHalfCloseUnsupported = errors.New("half-close unsupported")

type inboundConnHandler interface {
	ServeConnContext(context.Context, net.Conn)
}

const inboundDemuxSniffTimeout = 15 * time.Second
const (
	inboundDemuxAcceptRetryMinDelay = 5 * time.Millisecond
	inboundDemuxAcceptRetryMaxDelay = time.Second
)

type inboundDemuxServer struct {
	httpServer   *http.Server
	httpListener *connChannelListener
	socksHandler inboundConnHandler

	mu           sync.Mutex
	outer        net.Listener
	shuttingDown bool
	activeConns  map[net.Conn]struct{}
	sniffConns   map[net.Conn]struct{}
	workerWG     sync.WaitGroup
	baseCtx      context.Context
	cancelBase   context.CancelFunc
}

func newInboundDemuxServer(httpServer *http.Server, socksHandler inboundConnHandler) *inboundDemuxServer {
	if httpServer == nil {
		httpServer = &http.Server{Handler: http.NotFoundHandler()}
	}
	return &inboundDemuxServer{
		httpServer:   httpServer,
		httpListener: newConnChannelListener(),
		socksHandler: socksHandler,
		activeConns:  make(map[net.Conn]struct{}),
		sniffConns:   make(map[net.Conn]struct{}),
	}
}

func (s *inboundDemuxServer) Serve(ln net.Listener) error {
	if ln == nil {
		return net.ErrClosed
	}

	s.mu.Lock()
	s.outer = ln
	s.shuttingDown = false
	s.baseCtx, s.cancelBase = context.WithCancel(context.Background())
	s.mu.Unlock()

	go func() {
		_ = s.httpServer.Serve(s.httpListener)
	}()
	defer s.httpListener.Close()

	var tempDelay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.isShuttingDown() {
				return http.ErrServerClosed
			}
			if nextDelay, retry := inboundDemuxAcceptRetryDelay(err, tempDelay); retry {
				tempDelay = nextDelay
				time.Sleep(tempDelay)
				continue
			}
			return err
		}
		tempDelay = 0
		if !s.tryStartConnWorker() {
			_ = conn.Close()
			continue
		}
		go s.handleAcceptedConn(conn)
	}
	return nil
}

func inboundDemuxAcceptRetryDelay(err error, prev time.Duration) (time.Duration, bool) {
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Temporary() {
		return 0, false
	}
	if prev <= 0 {
		return inboundDemuxAcceptRetryMinDelay, true
	}
	next := prev * 2
	if next > inboundDemuxAcceptRetryMaxDelay {
		next = inboundDemuxAcceptRetryMaxDelay
	}
	return next, true
}

func (s *inboundDemuxServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.shuttingDown = true
	outer := s.outer
	cancelBase := s.cancelBase
	s.mu.Unlock()

	if cancelBase != nil {
		cancelBase()
	}
	if outer != nil {
		_ = outer.Close()
	}
	s.closeSniffConns()

	httpErr := error(nil)
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			httpErr = err
			_ = s.httpServer.Close()
		}
	}
	if ctx.Err() != nil {
		s.closeActiveConns()
	}

	waitDone := make(chan struct{})
	go func() {
		s.workerWG.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		if httpErr == nil {
			return ctx.Err()
		}
		return httpErr
	case <-ctx.Done():
		s.closeActiveConns()
		if s.httpServer != nil {
			_ = s.httpServer.Close()
		}
		<-waitDone
		if httpErr == nil {
			return ctx.Err()
		}
		return httpErr
	}
}

func (s *inboundDemuxServer) handleAcceptedConn(conn net.Conn) {
	defer s.workerWG.Done()
	s.trackActiveConn(conn)
	s.trackSniffConn(conn)

	reader := bufio.NewReader(conn)
	if err := conn.SetReadDeadline(time.Now().Add(inboundDemuxSniffTimeout)); err != nil {
		s.untrackSniffConn(conn)
		s.untrackActiveConn(conn)
		_ = conn.Close()
		return
	}
	first, err := reader.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		s.untrackSniffConn(conn)
		s.untrackActiveConn(conn)
		_ = conn.Close()
		return
	}
	s.untrackSniffConn(conn)

	bufferedConn, err := newPrebufferedConn(conn, reader)
	if err != nil {
		s.untrackActiveConn(conn)
		_ = conn.Close()
		return
	}

	if first[0] == 0x05 {
		if s.socksHandler == nil {
			s.untrackActiveConn(conn)
			_ = bufferedConn.Close()
			return
		}
		defer s.untrackActiveConn(conn)
		s.socksHandler.ServeConnContext(s.baseContext(), bufferedConn)
		return
	}

	httpConn := newCloseHookConn(bufferedConn, func() {
		s.untrackActiveConn(conn)
	})
	if err := s.httpListener.Enqueue(httpConn); err != nil {
		_ = httpConn.Close()
	}
}

func (s *inboundDemuxServer) baseContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baseCtx != nil {
		return s.baseCtx
	}
	return context.Background()
}

func (s *inboundDemuxServer) isShuttingDown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shuttingDown
}

func (s *inboundDemuxServer) tryStartConnWorker() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shuttingDown {
		return false
	}
	// Registering the worker while holding mu keeps new Add(1) calls serialized
	// with Shutdown() transitioning into workerWG.Wait().
	s.workerWG.Add(1)
	return true
}

func (s *inboundDemuxServer) trackActiveConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeConns == nil {
		s.activeConns = make(map[net.Conn]struct{})
	}
	s.activeConns[conn] = struct{}{}
}

func (s *inboundDemuxServer) untrackActiveConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeConns, conn)
}

func (s *inboundDemuxServer) trackSniffConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sniffConns == nil {
		s.sniffConns = make(map[net.Conn]struct{})
	}
	s.sniffConns[conn] = struct{}{}
}

func (s *inboundDemuxServer) untrackSniffConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sniffConns, conn)
}

func (s *inboundDemuxServer) closeSniffConns() {
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.sniffConns))
	for conn := range s.sniffConns {
		conns = append(conns, conn)
	}
	s.sniffConns = make(map[net.Conn]struct{})
	s.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (s *inboundDemuxServer) closeActiveConns() {
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.activeConns))
	for conn := range s.activeConns {
		conns = append(conns, conn)
	}
	s.activeConns = make(map[net.Conn]struct{})
	s.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}

type connChannelListener struct {
	mu     sync.Mutex
	cond   *sync.Cond
	conns  []net.Conn
	closed bool
}

const connChannelListenerCapacity = 128

func newConnChannelListener() *connChannelListener {
	l := &connChannelListener{
		conns: make([]net.Conn, 0, connChannelListenerCapacity),
	}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *connChannelListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for len(l.conns) == 0 && !l.closed {
		l.cond.Wait()
	}
	if len(l.conns) == 0 {
		return nil, net.ErrClosed
	}

	conn := l.conns[0]
	l.conns[0] = nil
	l.conns = l.conns[1:]
	l.cond.Signal()
	return conn, nil
}

func (l *connChannelListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	pending := append([]net.Conn(nil), l.conns...)
	l.conns = nil
	l.cond.Broadcast()
	l.mu.Unlock()

	for _, conn := range pending {
		if conn != nil {
			_ = conn.Close()
		}
	}
	return nil
}

func (l *connChannelListener) Addr() net.Addr {
	return connChannelAddr("resin-http-demux")
}

func (l *connChannelListener) Enqueue(conn net.Conn) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for len(l.conns) >= connChannelListenerCapacity && !l.closed {
		l.cond.Wait()
	}
	if l.closed {
		return net.ErrClosed
	}

	l.conns = append(l.conns, conn)
	l.cond.Signal()
	return nil
}

type connChannelAddr string

func (a connChannelAddr) Network() string { return "tcp" }

func (a connChannelAddr) String() string { return string(a) }

type prebufferedConn struct {
	net.Conn
	reader io.Reader
}

type closeHookConn struct {
	net.Conn
	onClose   func()
	closeOnce sync.Once
	closeErr  error
}

func newPrebufferedConn(conn net.Conn, reader *bufio.Reader) (net.Conn, error) {
	if conn == nil || reader == nil || reader.Buffered() == 0 {
		return conn, nil
	}
	prefetched := make([]byte, reader.Buffered())
	if _, err := io.ReadFull(reader, prefetched); err != nil {
		return nil, err
	}
	return &prebufferedConn{
		Conn:   conn,
		reader: io.MultiReader(bytes.NewReader(prefetched), conn),
	}, nil
}

func (c *prebufferedConn) Read(p []byte) (int, error) {
	if c == nil || c.reader == nil {
		return 0, net.ErrClosed
	}
	return c.reader.Read(p)
}

func (c *prebufferedConn) CloseWrite() error {
	if c == nil {
		return net.ErrClosed
	}
	return closeWriteErr(c.Conn)
}

func (c *prebufferedConn) CloseRead() error {
	if c == nil {
		return net.ErrClosed
	}
	return closeReadErr(c.Conn)
}

func newCloseHookConn(conn net.Conn, onClose func()) net.Conn {
	if conn == nil || onClose == nil {
		return conn
	}
	return &closeHookConn{
		Conn:    conn,
		onClose: onClose,
	}
}

func (c *closeHookConn) Close() error {
	if c == nil || c.Conn == nil {
		return net.ErrClosed
	}
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.closeErr
}

func (c *closeHookConn) CloseWrite() error {
	if c == nil {
		return net.ErrClosed
	}
	return closeWriteErr(c.Conn)
}

func (c *closeHookConn) CloseRead() error {
	if c == nil {
		return net.ErrClosed
	}
	return closeReadErr(c.Conn)
}

func closeWriteErr(conn net.Conn) error {
	if conn == nil {
		return net.ErrClosed
	}
	closeWriter, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		return errHalfCloseUnsupported
	}
	return closeWriter.CloseWrite()
}

func closeReadErr(conn net.Conn) error {
	if conn == nil {
		return net.ErrClosed
	}
	closeReader, ok := conn.(interface{ CloseRead() error })
	if !ok {
		return errHalfCloseUnsupported
	}
	return closeReader.CloseRead()
}
