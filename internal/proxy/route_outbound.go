package proxy

import (
	"encoding/json"
	"log"

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
		log.Printf("[proxy] route node=%s outbound=nil", result.NodeHash.Hex())
		return routedOutbound{}, ErrNoAvailableNodes
	}

	var raw map[string]any
	if err := json.Unmarshal(entry.RawOptions, &raw); err == nil {
		tag, _ := raw["tag"].(string)
		detour, _ := raw["detour"].(string)
		log.Printf("[proxy] route node=%s tag=%s detour=%s", result.NodeHash.Hex(), tag, detour)
	}

	return routedOutbound{
		Route:    result,
		Outbound: *obPtr,
	}, nil
}
