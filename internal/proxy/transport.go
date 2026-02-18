package proxy

import (
	"context"
	"net"
	"net/http"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

func newOutboundTransport(ob adapter.Outbound) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ob.DialContext(ctx, network, M.ParseSocksaddr(addr))
		},
		DisableKeepAlives: true,
	}
}
