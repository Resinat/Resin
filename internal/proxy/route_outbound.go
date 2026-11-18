package proxy

import (
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/sagernet/sing-box/adapter"
)

type routedOutbound struct {
	Route    routing.RouteResult
	Outbound adapter.Outbound
}

func resolveRoutedOutbound(
	router *routing.Router,
	pool outbound.PoolAccessor,
	platformName string,
	account string,
	target string,
) (routedOutbound, *ProxyError) {
	result, err := router.RouteRequest(platformName, account, target)
	if err != nil {
		return routedOutbound{}, mapRouteError(err)
	}

	entry, ok := pool.GetEntry(result.NodeHash)
	if !ok {
		return routedOutbound{}, ErrNoAvailableNodes
	}
	obPtr := entry.Outbound.Load()
	if obPtr == nil {
		return routedOutbound{}, ErrNoAvailableNodes
	}

	version := result.NodeVersion
	if ob, ok := (*obPtr).(interface{ RoutingVersion() uint64 }); ok {
		if ob.RoutingVersion() != version {
			return routedOutbound{}, ErrNoAvailableNodes
		}
	}
	if version == 0 || version != entry.RoutingVersion() {
		return routedOutbound{}, ErrNoAvailableNodes
	}
	if chained, ok := (*obPtr).(interface{ ChainProxy() bool }); ok {
		result.ChainProxy = chained.ChainProxy()
	}

	return routedOutbound{
		Route:    result,
		Outbound: *obPtr,
	}, nil
}
