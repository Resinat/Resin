package proxy

import (
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/topology"
	"github.com/sagernet/sing-box/adapter"
)

type feedbackCapture struct{ events chan node.Feedback }

func (c *feedbackCapture) ApplyFeedback(_ node.Hash, _ uint64, f node.Feedback) bool {
	c.events <- f
	return true
}

func TestZeroVersionFeedbackIsRejected(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{MaxConsecutiveFailures: func() int { return 1 }})
	raw := []byte(`{"type":"http","server":"landing"}`)
	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, "sub")
	success := true
	if pool.ApplyFeedback(hash, 0, node.Feedback{Result: &success}) {
		t.Fatal("zero version bypassed feedback isolation")
	}
	entry, _ := pool.GetEntry(hash)
	if !entry.IsCircuitOpen() {
		t.Fatal("zero version restored health")
	}
}

func TestResolveRejectsModeChangeAfterRouteSelection(t *testing.T) {
	env := newProxyE2EEnv(t)
	router := routing.NewRouter(routing.RouterConfig{Pool: env.pool, Authorities: func() []string { return nil }, P2CWindow: func() time.Duration { return time.Minute }, NodeTagResolver: func(hash node.Hash) string {
		// This callback runs after the route and its version were selected, but
		// before resolveRoutedOutbound obtains the current outbound.
		entry, _ := env.pool.GetEntry(hash)
		entry.UpdateRoutingRole(node.RoleChain)
		replacement := new(versionedTestOutbound)
		replacement.version.Store(entry.RoutingVersion())
		var ob adapter.Outbound = replacement
		entry.Outbound.Store(&ob)
		return "node"
	}})
	if _, err := resolveRoutedOutbound(router, env.pool, "plat", "", "example.com:443"); err != ErrNoAvailableNodes {
		t.Fatalf("new mode silently replaced the selected route: %v", err)
	}
}
