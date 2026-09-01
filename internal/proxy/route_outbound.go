package proxy

import (
	"strings"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/sagernet/sing-box/adapter"
)

type routeOverride struct {
	NodeHash string
}

type healthyEnabledEvaluator interface {
	MakeHealthyAndEnabledEvaluator() func(*node.NodeEntry) bool
}

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
	override routeOverride,
) (routedOutbound, *ProxyError) {
	if strings.TrimSpace(override.NodeHash) != "" {
		return resolveNodeHashOverrideOutbound(pool, override.NodeHash)
	}

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

	return routedOutbound{
		Route:    result,
		Outbound: *obPtr,
	}, nil
}

func resolveNodeHashOverrideOutbound(pool outbound.PoolAccessor, nodeHash string) (routedOutbound, *ProxyError) {
	h, err := node.ParseHex(strings.TrimSpace(nodeHash))
	if err != nil {
		return routedOutbound{}, ErrNoAvailableNodes
	}

	entry, ok := pool.GetEntry(h)
	if !ok || !routeOverrideEntryAvailable(pool, entry) {
		return routedOutbound{}, ErrNoAvailableNodes
	}
	obPtr := entry.Outbound.Load()
	if obPtr == nil {
		return routedOutbound{}, ErrNoAvailableNodes
	}

	return routedOutbound{
		Route: routing.RouteResult{
			NodeHash: h,
			EgressIP: entry.GetEgressIP(),
		},
		Outbound: *obPtr,
	}, nil
}

func routeOverrideEntryAvailable(pool outbound.PoolAccessor, entry *node.NodeEntry) bool {
	if entry == nil {
		return false
	}
	if evaluatorPool, ok := pool.(healthyEnabledEvaluator); ok {
		return evaluatorPool.MakeHealthyAndEnabledEvaluator()(entry)
	}
	return entry.IsHealthy()
}
