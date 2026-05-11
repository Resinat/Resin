package proxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/routing"
	M "github.com/sagernet/sing/common/metadata"
)

type tunnelDeps struct {
	router      *routing.Router
	pool        outbound.PoolAccessor
	health      HealthRecorder
	metricsSink MetricsEventSink
}

type preparedTunnel struct {
	upstreamConn net.Conn
	recordResult func(bool)
}

type tunnelPrepareResult struct {
	route         routing.RouteResult
	session       *preparedTunnel
	proxyErr      *ProxyError
	upstreamStage string
	upstreamErr   error
	canceled      bool
}

type tunnelRelayResult struct {
	ingressBytes  int64
	egressBytes   int64
	netOK         bool
	proxyErr      *ProxyError
	upstreamStage string
	upstreamErr   error
}

type tunnelPumpOptions struct {
	requireBidirectionalTraffic bool
}

func prepareConnectTunnel(
	ctx context.Context,
	deps tunnelDeps,
	platformName string,
	account string,
	target string,
) tunnelPrepareResult {
	routed, routeErr := resolveRoutedOutbound(deps.router, deps.pool, platformName, account, target)
	if routeErr != nil {
		return tunnelPrepareResult{proxyErr: routeErr}
	}

	domain := netutil.ExtractDomain(target)
	nodeHashRaw := routed.Route.NodeHash
	if deps.health != nil {
		go deps.health.RecordLatency(nodeHashRaw, domain, nil)
	}

	rawConn, err := routed.Outbound.DialContext(ctx, "tcp", M.ParseSocksaddr(target))
	if err != nil {
		proxyErr := classifyConnectError(err)
		if proxyErr == nil {
			return tunnelPrepareResult{
				route:    routed.Route,
				canceled: true,
			}
		}
		if deps.health != nil {
			go deps.health.RecordResult(nodeHashRaw, false)
		}
		return tunnelPrepareResult{
			route:         routed.Route,
			proxyErr:      proxyErr,
			upstreamStage: "connect_dial",
			upstreamErr:   err,
		}
	}

	recordResult := func(ok bool) {
		if deps.health != nil {
			go deps.health.RecordResult(nodeHashRaw, ok)
		}
	}

	var upstreamBase net.Conn = rawConn
	if deps.metricsSink != nil {
		deps.metricsSink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
		upstreamBase = newCountingConn(rawConn, deps.metricsSink)
	}

	upstreamConn := newTLSLatencyConn(upstreamBase, func(latency time.Duration) {
		if deps.health != nil {
			deps.health.RecordLatency(nodeHashRaw, domain, &latency)
		}
	})

	return tunnelPrepareResult{
		route: routed.Route,
		session: &preparedTunnel{
			upstreamConn: upstreamConn,
			recordResult: recordResult,
		},
	}
}

func pumpPreparedTunnel(
	clientConn net.Conn,
	clientReader *bufio.Reader,
	session *preparedTunnel,
	opts tunnelPumpOptions,
) tunnelRelayResult {
	clientToUpstream, err := makeTunnelClientReader(clientConn, clientReader)
	if err != nil {
		if session != nil && session.upstreamConn != nil {
			_ = session.upstreamConn.Close()
		}
		if clientConn != nil {
			_ = clientConn.Close()
		}
		return tunnelRelayResult{
			proxyErr:      ErrUpstreamRequestFailed,
			upstreamStage: "connect_client_prefetch_drain",
			upstreamErr:   err,
		}
	}
	return pumpPreparedTunnelReader(clientConn, clientToUpstream, session, opts)
}

func pumpPreparedTunnelReader(
	clientConn net.Conn,
	clientToUpstream io.Reader,
	session *preparedTunnel,
	opts tunnelPumpOptions,
) tunnelRelayResult {
	if clientConn == nil || clientToUpstream == nil || session == nil || session.upstreamConn == nil {
		return tunnelRelayResult{}
	}

	type copyResult struct {
		n   int64
		err error
	}
	var closeBothOnce sync.Once
	closeBoth := func() {
		closeBothOnce.Do(func() {
			_ = clientConn.Close()
			_ = session.upstreamConn.Close()
		})
	}
	ingressBytesCh := make(chan copyResult, 1)
	egressBytesCh := make(chan copyResult, 1)
	go func() {
		n, copyErr := io.Copy(session.upstreamConn, clientToUpstream)
		if !isBenignTunnelCopyError(copyErr) || !closeWriteConn(session.upstreamConn) {
			closeBoth()
		}
		egressBytesCh <- copyResult{n: n, err: copyErr}
	}()
	go func() {
		n, copyErr := io.Copy(clientConn, session.upstreamConn)
		if !isBenignTunnelCopyError(copyErr) || !closeWriteConn(clientConn) {
			closeBoth()
		}
		ingressBytesCh <- copyResult{n: n, err: copyErr}
	}()

	ingressResult := <-ingressBytesCh
	egressResult := <-egressBytesCh
	closeBoth()

	ingressErrBenign := isBenignTunnelCopyError(ingressResult.err)
	egressErrBenign := isBenignTunnelCopyError(egressResult.err)
	// A client-side TCP reset after the upstream response has already started is
	// a shutdown artifact, not an upstream failure. This commonly happens when a
	// tunnel client exits immediately after consuming the response.
	if !egressErrBenign && ingressResult.n > 0 && isClientReadResetError(egressResult.err) {
		egressErrBenign = true
	}

	result := tunnelRelayResult{
		ingressBytes: ingressResult.n,
		egressBytes:  egressResult.n,
		netOK:        true,
	}
	switch {
	case !ingressErrBenign:
		result.netOK = false
		result.proxyErr = ErrUpstreamRequestFailed
		result.upstreamStage = "connect_upstream_to_client_copy"
		result.upstreamErr = ingressResult.err
	case !egressErrBenign:
		result.netOK = false
		result.proxyErr = ErrUpstreamRequestFailed
		result.upstreamStage = "connect_client_to_upstream_copy"
		result.upstreamErr = egressResult.err
	case opts.requireBidirectionalTraffic && (ingressResult.n == 0 || egressResult.n == 0):
		result.netOK = false
		result.proxyErr = ErrUpstreamRequestFailed
		switch {
		case ingressResult.n == 0 && egressResult.n == 0:
			result.upstreamStage = "connect_zero_traffic"
		case ingressResult.n == 0:
			result.upstreamStage = "connect_no_ingress_traffic"
		default:
			result.upstreamStage = "connect_no_egress_traffic"
		}
	}
	return result
}

func closeWriteConn(conn net.Conn) bool {
	return closeWriteErr(conn) == nil
}

// makeTunnelClientReader returns a reader for client->upstream copy that
// preserves any bytes already buffered by a protocol reader before tunneling.
func makeTunnelClientReader(clientConn net.Conn, buffered *bufio.Reader) (io.Reader, error) {
	if buffered == nil {
		return clientConn, nil
	}
	n := buffered.Buffered()
	if n == 0 {
		return clientConn, nil
	}
	prefetched := make([]byte, n)
	if _, err := io.ReadFull(buffered, prefetched); err != nil {
		return nil, err
	}
	return io.MultiReader(bytes.NewReader(prefetched), clientConn), nil
}
