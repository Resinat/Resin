package proxy

import (
	"context"
	"net"
	"net/http"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

func newOutboundTransport(ob adapter.Outbound, sink MetricsEventSink, platformID string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := ob.DialContext(ctx, network, M.ParseSocksaddr(addr))
			if err != nil {
				return nil, err
			}
			if sink != nil {
				sink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
				conn = newCountingConn(conn, sink, platformID)
			}
			return conn, nil
		},
		DisableKeepAlives: true,
	}
}
