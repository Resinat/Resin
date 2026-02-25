package main

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/testutil"
)

func TestNodePoolStatsAdapter_HealthyNodesRequiresOutbound(t *testing.T) {
	_, pool := newBootstrapTestRuntime(config.NewDefaultRuntimeConfig())
	adapter := &runtimeStatsAdapter{pool: pool}

	healthyHash := node.HashFromRawOptions([]byte(`{"type":"direct","server":"1.1.1.1","port":443}`))
	healthy := node.NewNodeEntry(healthyHash, nil, time.Now(), 0)
	healthyOb := testutil.NewNoopOutbound()
	healthy.Outbound.Store(&healthyOb)
	healthy.SetEgressIP(netip.MustParseAddr("203.0.113.10"))
	pool.LoadNodeFromBootstrap(healthy)

	noOutboundHash := node.HashFromRawOptions([]byte(`{"type":"direct","server":"2.2.2.2","port":443}`))
	noOutbound := node.NewNodeEntry(noOutboundHash, nil, time.Now(), 0)
	noOutbound.SetEgressIP(netip.MustParseAddr("203.0.113.10"))
	pool.LoadNodeFromBootstrap(noOutbound)

	circuitOpenHash := node.HashFromRawOptions([]byte(`{"type":"direct","server":"3.3.3.3","port":443}`))
	circuitOpen := node.NewNodeEntry(circuitOpenHash, nil, time.Now(), 0)
	circuitOpenOb := testutil.NewNoopOutbound()
	circuitOpen.Outbound.Store(&circuitOpenOb)
	circuitOpen.SetEgressIP(netip.MustParseAddr("203.0.113.11"))
	circuitOpen.CircuitOpenSince.Store(time.Now().UnixNano())
	pool.LoadNodeFromBootstrap(circuitOpen)

	if got, want := adapter.HealthyNodes(), 1; got != want {
		t.Fatalf("healthy_nodes: got %d, want %d", got, want)
	}
	if got, want := adapter.UniqueHealthyEgressIPCount(), 1; got != want {
		t.Fatalf("unique_healthy_egress_ips: got %d, want %d", got, want)
	}
}
