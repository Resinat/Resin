package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/proxy"
	"github.com/Resinat/Resin/internal/service"
)

type managedEndpointRuntime struct {
	config   atomic.Pointer[model.Endpoint]
	server   *inboundDemuxServer
	listener net.Listener
	started  bool
}

func (r *managedEndpointRuntime) current() model.Endpoint {
	if r == nil {
		return model.Endpoint{}
	}
	endpoint := r.config.Load()
	if endpoint == nil {
		return model.Endpoint{}
	}
	return *endpoint
}

type endpointRuntimeManager struct {
	mu            sync.Mutex
	listenAddress string
	proxyToken    string
	forward       http.Handler
	reverse       http.Handler
	apiHandler    http.Handler
	tokenAPI      http.Handler
	socks5        inboundConnHandler
	metricsSink   proxy.MetricsEventSink
	runtimes      map[string]*managedEndpointRuntime
	statuses      map[string]service.EndpointRuntimeStatus
	started       bool
	stopping      bool
	serverErrCh   chan error
}

func newEndpointRuntimeManager(
	listenAddress string,
	proxyToken string,
	forward, reverse, apiHandler, tokenAPI http.Handler,
	socks5 inboundConnHandler,
	metricsSink proxy.MetricsEventSink,
) *endpointRuntimeManager {
	return &endpointRuntimeManager{
		listenAddress: listenAddress,
		proxyToken:    proxyToken,
		forward:       forward,
		reverse:       reverse,
		apiHandler:    apiHandler,
		tokenAPI:      tokenAPI,
		socks5:        socks5,
		metricsSink:   metricsSink,
		runtimes:      make(map[string]*managedEndpointRuntime),
		statuses:      make(map[string]service.EndpointRuntimeStatus),
		serverErrCh:   make(chan error, 1),
	}
}

func (m *endpointRuntimeManager) ApplyEndpoint(endpoint model.Endpoint) error {
	if m == nil {
		return fmt.Errorf("endpoint runtime manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return net.ErrClosed
	}

	if current := m.runtimes[endpoint.ID]; current != nil && current.current().Port == endpoint.Port {
		copy := endpoint
		current.config.Store(&copy)
		if current.started {
			m.statuses[endpoint.ID] = service.EndpointRuntimeStatus{State: "active"}
		} else {
			m.statuses[endpoint.ID] = service.EndpointRuntimeStatus{State: "starting"}
		}
		return nil
	}

	listener, err := net.Listen("tcp", formatListenAddress(m.listenAddress, endpoint.Port))
	if err != nil {
		if m.runtimes[endpoint.ID] == nil {
			m.statuses[endpoint.ID] = service.EndpointRuntimeStatus{State: "error", LastError: err.Error()}
		}
		return err
	}
	listener = proxy.NewCountingListener(listener, m.metricsSink)

	runtime := &managedEndpointRuntime{listener: listener}
	copy := endpoint
	runtime.config.Store(&copy)
	currentConfig := func() model.Endpoint { return runtime.current() }
	httpHandler := newEndpointInboundMux(
		currentConfig,
		m.proxyToken,
		m.forward,
		m.reverse,
		m.apiHandler,
		m.tokenAPI,
	)
	runtime.server = newInboundDemuxServer(
		&http.Server{Handler: httpHandler},
		&endpointSocksGate{current: currentConfig, next: m.socks5},
	)

	old := m.runtimes[endpoint.ID]
	m.runtimes[endpoint.ID] = runtime
	m.statuses[endpoint.ID] = service.EndpointRuntimeStatus{State: "starting"}
	if m.started {
		m.startRuntimeLocked(runtime)
	}
	if old != nil {
		go stopManagedEndpoint(old, 5*time.Second)
	}
	return nil
}

func (m *endpointRuntimeManager) RemoveEndpoint(id string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	runtime := m.runtimes[id]
	delete(m.runtimes, id)
	delete(m.statuses, id)
	m.mu.Unlock()
	if runtime != nil {
		// Release the port before returning so a following start can rebind it.
		_ = runtime.listener.Close()
		go stopManagedEndpoint(runtime, 5*time.Second)
	}
}

func (m *endpointRuntimeManager) EndpointStatus(id string) service.EndpointRuntimeStatus {
	if m == nil {
		return service.EndpointRuntimeStatus{State: "inactive"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.statuses[id]
	if status.State == "" {
		status.State = "inactive"
	}
	return status
}

func (m *endpointRuntimeManager) RecordEndpointError(endpoint model.Endpoint, err error) {
	if m == nil || err == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[endpoint.ID] = service.EndpointRuntimeStatus{State: "error", LastError: err.Error()}
}

func (m *endpointRuntimeManager) Start() <-chan error {
	m.mu.Lock()
	if !m.started && !m.stopping {
		m.started = true
		for _, runtime := range m.runtimes {
			m.startRuntimeLocked(runtime)
		}
	}
	m.mu.Unlock()
	return m.serverErrCh
}

func (m *endpointRuntimeManager) startRuntimeLocked(runtime *managedEndpointRuntime) {
	if runtime == nil || runtime.started {
		return
	}
	runtime.started = true
	endpoint := runtime.current()
	m.statuses[endpoint.ID] = service.EndpointRuntimeStatus{State: "active"}
	log.Printf("Endpoint %s starting on %s", endpoint.ID, formatListenAddress(m.listenAddress, endpoint.Port))
	go func() {
		err := runtime.server.Serve(runtime.listener)
		if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return
		}
		m.handleRuntimeError(runtime, err)
	}()
}

func (m *endpointRuntimeManager) handleRuntimeError(runtime *managedEndpointRuntime, err error) {
	endpoint := runtime.current()
	m.mu.Lock()
	if m.runtimes[endpoint.ID] == runtime {
		delete(m.runtimes, endpoint.ID)
		m.statuses[endpoint.ID] = service.EndpointRuntimeStatus{State: "error", LastError: err.Error()}
	}
	stopping := m.stopping
	m.mu.Unlock()
	if !stopping && endpoint.ID == service.DefaultEndpointID {
		select {
		case m.serverErrCh <- fmt.Errorf("default endpoint: %w", err):
		default:
		}
	}
}

func (m *endpointRuntimeManager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.stopping = true
	runtimes := make([]*managedEndpointRuntime, 0, len(m.runtimes))
	for _, runtime := range m.runtimes {
		runtimes = append(runtimes, runtime)
	}
	m.runtimes = make(map[string]*managedEndpointRuntime)
	m.mu.Unlock()

	errCh := make(chan error, len(runtimes))
	var wg sync.WaitGroup
	for _, runtime := range runtimes {
		wg.Add(1)
		go func(runtime *managedEndpointRuntime) {
			defer wg.Done()
			if err := runtime.server.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				errCh <- err
			}
			_ = runtime.listener.Close()
		}(runtime)
	}
	wg.Wait()
	close(errCh)
	var shutdownErrs []error
	for err := range errCh {
		shutdownErrs = append(shutdownErrs, err)
	}
	return errors.Join(shutdownErrs...)
}

func stopManagedEndpoint(runtime *managedEndpointRuntime, timeout time.Duration) {
	if runtime == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = runtime.server.Shutdown(ctx)
	_ = runtime.listener.Close()
}

type endpointSocksGate struct {
	current func() model.Endpoint
	next    inboundConnHandler
}

func (g *endpointSocksGate) ServeConnContext(ctx context.Context, conn net.Conn) {
	endpoint := model.Endpoint{}
	if g != nil && g.current != nil {
		endpoint = g.current()
	}
	if !endpoint.AllowProxy || !endpoint.AllowSOCKS5 || g == nil || g.next == nil {
		if conn != nil {
			_, _ = conn.Write([]byte{0x05, 0xFF})
			_ = conn.Close()
		}
		return
	}
	ctx = proxy.ContextWithInboundPolicy(ctx, proxy.InboundPolicy{
		RequireProxyAuthInfo: endpoint.RequireProxyAuthInfo,
	})
	g.next.ServeConnContext(ctx, conn)
}
