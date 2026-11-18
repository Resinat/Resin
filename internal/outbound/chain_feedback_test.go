package outbound

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/probe"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
)

func newChainFeedbackPool(t *testing.T) (*topology.GlobalNodePool, *node.NodeEntry, *node.NodeEntry) {
	t.Helper()
	jump, exit := newChainHTTPProxy(t), newChainHTTPProxy(t)
	subs := topology.NewSubscriptionManager()
	landing := subscription.NewSubscription("landing", "landing", "", true, false)
	landing.SetChainConfig(true, false)
	relay := subscription.NewSubscription("relay", "relay", "", true, false)
	relay.SetChainConfig(false, true)
	subs.Register(landing)
	subs.Register(relay)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxConsecutiveFailures: func() int { return 2 }})
	builder := newTestSingboxBuilder(t)
	t.Cleanup(func() { builder.Close() })
	mgr := NewOutboundManager(pool, builder)
	t.Cleanup(func() { mgr.Close() })
	mgr.SetRelayPicker(routing.NewRelaySelector(pool, nil, nil).Pick)
	add := func(sub *subscription.Subscription, p *chainHTTPProxy) *node.NodeEntry {
		raw := p.raw(t)
		hash := node.HashFromRawOptions(raw)
		sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{})
		pool.AddNodeFromSub(hash, raw, sub.ID)
		mgr.EnsureNodeOutbound(hash)
		pool.RecordResult(hash, true)
		entry, _ := pool.GetEntry(hash)
		return entry
	}
	return pool, add(landing, exit), add(relay, jump)
}

func TestChainProbeFeedbackRetainsActualRelay(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		truncated bool
	}{{"success", false}, {"truncated", true}} {
		truncated := scenario.truncated
		for _, kind := range []string{"egress", "latency"} {
			t.Run(kind+"/"+scenario.name, func(t *testing.T) {
				pool, landing, relay := newChainFeedbackPool(t)
				failed := false
				pool.ApplyFeedback(landing.Hash, landing.RoutingVersion(), node.Feedback{
					Result: &failed,
					Err: &node.RelayError{Hash: relay.Hash, Version: relay.RoutingVersion(), ServerDial: true,
						Err: errors.New("previous first-hop failure")},
				})
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if truncated {
						w.Header().Set("Content-Length", "100")
						io.WriteString(w, "short")
					} else {
						io.WriteString(w, "ip=203.0.113.1\nloc=US\n")
					}
				}))
				defer target.Close()
				probes := 0
				pool.SetOnRelaySuspect(func(hash node.Hash) {
					if hash != relay.Hash {
						t.Fatalf("wrong relay probed: %s", hash)
					}
					probes++
				})
				m := probe.NewProbeManager(probe.ProbeConfig{Pool: pool, Fetcher: func(hash node.Hash, _ string) (netutil.OutboundHTTPResult, error) {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					entry, _ := pool.GetEntry(hash)
					// Include the production probe accounting wrapper; it must preserve identity.
					return netutil.HTTPGetViaOutbound(ctx, *entry.Outbound.Load(), target.URL, netutil.OutboundHTTPOptions{
						OnConnLifecycle: func(netutil.ConnLifecycleOp) {},
					})
				}})
				defer m.Stop()
				var err error
				if kind == "egress" {
					_, err = m.ProbeEgressSync(landing.Hash)
				} else {
					_, err = m.ProbeLatencySync(landing.Hash)
				}
				if truncated {
					var evidence *node.RelayError
					if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.As(err, &evidence) || evidence.ServerDial || evidence.Hash != relay.Hash || evidence.Version != relay.RoutingVersion() {
						t.Fatalf("truncated probe lost actual relay: %v", err)
					}
					if probes != 1 || landing.FailureCount.Load() != 1 || relay.FailureCount.Load() != 1 {
						t.Fatalf("incorrect uncertain feedback: probes=%d landing=%d relay=%d", probes, landing.FailureCount.Load(), relay.FailureCount.Load())
					}
				} else {
					if err != nil || relay.FailureCount.Load() != 0 || landing.FailureCount.Load() != 0 || probes != 0 {
						t.Fatalf("success did not reset actual relay: err=%v landing=%d relay=%d probes=%d", err, landing.FailureCount.Load(), relay.FailureCount.Load(), probes)
					}
					pool.ApplyFeedback(landing.Hash, landing.RoutingVersion(), node.Feedback{Result: &failed,
						Err: &node.RelayError{Hash: relay.Hash, Version: relay.RoutingVersion(), ServerDial: true, Err: io.ErrUnexpectedEOF}})
					if relay.FailureCount.Load() != 1 || relay.IsCircuitOpen() {
						t.Fatal("successful chain probe did not break consecutive relay failures")
					}
				}
			})
		}
	}
}
