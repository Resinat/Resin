package main

import (
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/testutil"
)

func TestNodePoolStatsAdapter_HealthyNodesRequiresOutbound(t *testing.T) {
	_, pool := newBootstrapTestRuntime(config.NewDefaultRuntimeConfig())
	adapter := &runtimeStatsAdapter{pool: pool}

	healthyHash := node.HashFromRawOptions([]byte(`{"type":"direct","server":"1.1.1.1","port":443}`))
	healthy := node.NewNodeEntry(healthyHash, nil, time.Now(), 0)
	healthyOb := testutil.NewNoopOutbound()
	healthy.Outbound.Store(&healthyOb)
	pool.LoadNodeFromBootstrap(healthy)

	noOutboundHash := node.HashFromRawOptions([]byte(`{"type":"direct","server":"2.2.2.2","port":443}`))
	noOutbound := node.NewNodeEntry(noOutboundHash, nil, time.Now(), 0)
	pool.LoadNodeFromBootstrap(noOutbound)

	circuitOpenHash := node.HashFromRawOptions([]byte(`{"type":"direct","server":"3.3.3.3","port":443}`))
	circuitOpen := node.NewNodeEntry(circuitOpenHash, nil, time.Now(), 0)
	circuitOpenOb := testutil.NewNoopOutbound()
	circuitOpen.Outbound.Store(&circuitOpenOb)
	circuitOpen.CircuitOpenSince.Store(time.Now().UnixNano())
	pool.LoadNodeFromBootstrap(circuitOpen)

	if got, want := adapter.HealthyNodes(), 1; got != want {
		t.Fatalf("healthy_nodes: got %d, want %d", got, want)
	}
}
