package topology

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/subscription"
)

func TestRelayFailureFeedbackAttribution(t *testing.T) {
	for _, scenario := range []string{"server dial", "uncertain", "passive disabled", "stale relay", "stale landing", "zero relay version", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			pool, subs := newHealthTestPool(2)
			landingSub := subs.Lookup("s1")
			landingSub.SetChainConfig(true, false)
			relaySub := subscription.NewSubscription("r", "r", "", true, false)
			relaySub.SetChainConfig(false, true)
			subs.Register(relaySub)
			landing := addChainMember(pool, landingSub, `{"type":"http","server":"landing"}`)
			raw := `{"type":"http","server":"relay"}`
			relay := addChainMember(pool, relaySub, raw)
			l, _ := pool.GetEntry(landing)
			r, _ := pool.GetEntry(relay)
			pool.RecordResult(landing, true)
			pool.RecordResult(relay, true)
			version := l.RoutingVersion()
			evidence := &node.RelayError{Hash: relay, Version: r.RoutingVersion(), ServerDial: true, Err: io.ErrUnexpectedEOF}
			feedback := node.Feedback{Result: boolPtr(false), Target: "speed.example", Egress: true}
			wantLanding, wantRelay, wantProbes := int32(0), int32(1), 0
			switch scenario {
			case "uncertain":
				evidence.ServerDial = false
				wantLanding, wantRelay, wantProbes = 1, 0, 1
			case "passive disabled":
				p := platform.NewPlatform("p", "p", nil, nil)
				p.PassiveCircuitBreakerDisabled = true
				pool.RegisterPlatform(p)
				feedback.PlatformID = p.ID
				wantRelay = 0
			case "stale relay":
				pool.RemoveNodeFromSub(relay, relaySub.ID)
				addChainMember(pool, relaySub, raw)
				r, _ = pool.GetEntry(relay)
				pool.RecordResult(relay, true)
				wantRelay = 0
			case "stale landing":
				l.UpdateRoutingRole(node.RoleOrdinary)
				wantRelay = 0
			case "zero relay version":
				evidence.Version = 0
				wantRelay = 0
			case "canceled":
				evidence.Err = context.Canceled
				wantRelay = 0
			}
			feedback.Err = fmt.Errorf("request: %w", evidence)
			message := feedback.Err.Error()
			feedback.LastError = &message
			probes := 0
			pool.SetOnRelaySuspect(func(hash node.Hash) {
				if hash != relay {
					t.Fatalf("probed wrong relay: %s", hash)
				}
				probes++
				// This callback must run outside the node table operation.
				pool.ApplyFeedback(hash, r.RoutingVersion(), node.Feedback{Result: boolPtr(true)})
			})
			applied := pool.ApplyFeedback(landing, version, feedback)
			if applied != (scenario != "stale landing") {
				t.Fatalf("accepted stale landing feedback: %v", applied)
			}
			if l.FailureCount.Load() != wantLanding || r.FailureCount.Load() != wantRelay || probes != wantProbes {
				t.Fatalf("landing=%d relay=%d probes=%d", l.FailureCount.Load(), r.FailureCount.Load(), probes)
			}
			if applied && (l.LastEgressUpdateAttempt.Load() == 0 || l.LastLatencyProbeAttempt.Load() == 0 || l.GetLastError() == "") {
				t.Fatal("attribution dropped landing probe scheduling or error details")
			}
			if scenario == "passive disabled" {
				feedback.PlatformID = ""
				pool.ApplyFeedback(landing, version, feedback)
				if r.FailureCount.Load() != 1 {
					t.Fatal("platform setting disabled active relay probe feedback")
				}
			}
		})
	}
}

func TestStaleUncertainRelayFailureDoesNotProbeReplacement(t *testing.T) {
	pool, subs := newHealthTestPool(2)
	sub := subs.Lookup("s1")
	sub.SetChainConfig(false, true)
	hash := addChainMember(pool, sub, `{"type":"http","server":"relay"}`)
	r, _ := pool.GetEntry(hash)
	evidence := &node.RelayError{Hash: hash, Version: r.RoutingVersion(), Err: io.ErrUnexpectedEOF}
	pool.RemoveNodeFromSub(hash, sub.ID)
	addChainMember(pool, sub, `{"type":"http","server":"relay"}`)
	landing := addChainMember(pool, sub, `{"type":"http","server":"landing"}`)
	l, _ := pool.GetEntry(landing)
	pool.SetOnRelaySuspect(func(node.Hash) { t.Fatal("stale error scheduled a replacement relay probe") })
	pool.ApplyFeedback(landing, l.RoutingVersion(), node.Feedback{Result: boolPtr(false), Err: evidence})
}

func TestRelaySuccessResetsOnlyMatchingOutbound(t *testing.T) {
	for _, scenario := range []string{"success", "passive disabled", "stale relay", "stale landing", "unknown relay", "zero relay version", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			pool, subs := newHealthTestPool(2)
			sub := subs.Lookup("s1")
			landing := addChainMember(pool, sub, `{"type":"http","server":"landing"}`)
			raw := `{"type":"http","server":"relay"}`
			relay := addChainMember(pool, sub, raw)
			other := addChainMember(pool, sub, `{"type":"http","server":"other"}`)
			l, _ := pool.GetEntry(landing)
			r, _ := pool.GetEntry(relay)
			pool.RecordResult(landing, true)
			pool.RecordResult(relay, true)
			pool.RecordResult(other, true)
			pool.RecordResult(other, false)
			version := l.RoutingVersion()
			info := node.RelayInfo{Hash: relay, Version: r.RoutingVersion()}
			f := node.Feedback{Result: boolPtr(true), Relay: info}
			wantReset := scenario == "success" || scenario == "passive disabled"
			switch scenario {
			case "passive disabled":
				p := platform.NewPlatform("p", "p", nil, nil)
				p.PassiveCircuitBreakerDisabled = true
				pool.RegisterPlatform(p)
				f.PlatformID = p.ID
			case "stale relay":
				pool.RemoveNodeFromSub(relay, sub.ID)
				addChainMember(pool, sub, raw)
				r, _ = pool.GetEntry(relay)
				pool.RecordResult(relay, true)
			case "stale landing":
				l.UpdateRoutingRole(node.RoleChain)
			case "unknown relay":
				f.Relay = node.RelayInfo{}
			case "zero relay version":
				f.Relay.Version = 0
			case "canceled":
				f.Err = context.Canceled
			}
			pool.RecordResult(relay, false)
			pool.ApplyFeedback(landing, version, f)
			if (r.FailureCount.Load() == 0) != wantReset {
				t.Fatalf("relay failure count=%d, want reset=%v", r.FailureCount.Load(), wantReset)
			}
			o, _ := pool.GetEntry(other)
			if o.FailureCount.Load() != 1 {
				t.Fatal("success reset a different relay")
			}
			if wantReset {
				pool.RecordResult(relay, false)
				if r.FailureCount.Load() != 1 || r.IsCircuitOpen() {
					t.Fatal("failures separated by success were treated as consecutive")
				}
			}
		})
	}
}
