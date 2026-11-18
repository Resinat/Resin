package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/probe"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
)

func nodeRelayHTTPProxy(t *testing.T) json.RawMessage {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := (&net.Dialer{Timeout: time.Second}).DialContext(r.Context(), "tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		upstream.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		done := make(chan struct{})
		go func() { io.Copy(upstream, buffered); upstream.Close(); close(done) }()
		io.Copy(conn, upstream)
		conn.Close()
		<-done
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().(*net.TCPAddr)
	raw, err := json.Marshal(map[string]any{"type": "http", "server": "127.0.0.1", "server_port": addr.Port})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNodeRelayAllocationUpdatesAfterManualProbe(t *testing.T) {
	for _, kind := range []string{"egress", "latency"} {
		t.Run(kind, func(t *testing.T) {
			srv, cp, _ := newControlPlaneTestServer(t)
			landingSub := subscription.NewSubscription("landing", "Landing", "", true, false)
			landingSub.SetChainConfig(true, false)
			relaySub := subscription.NewSubscription("relay", "Relay", "", true, false)
			relaySub.SetChainConfig(false, true)
			cp.SubMgr.Register(landingSub)
			cp.SubMgr.Register(relaySub)
			builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{DNSUpstreams: []string{"local"}})
			if err != nil {
				t.Fatal(err)
			}
			defer builder.Close()
			mgr := outbound.NewOutboundManager(cp.Pool, builder)
			defer mgr.Close()
			mgr.SetRelayPicker(routing.NewRelaySelector(cp.Pool, nil, nil).Pick)
			add := func(sub *subscription.Subscription, tag string) node.Hash {
				raw := nodeRelayHTTPProxy(t)
				hash := node.HashFromRawOptions(raw)
				sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{tag}})
				cp.Pool.AddNodeFromSub(hash, raw, sub.ID)
				mgr.EnsureNodeOutbound(hash)
				cp.Pool.RecordResult(hash, true)
				return hash
			}
			landing, relay := add(landingSub, "Exit"), add(relaySub, "Jump")
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "ip=203.0.113.1\nloc=US\n")
			}))
			defer target.Close()
			cp.ProbeMgr = probe.NewProbeManager(probe.ProbeConfig{Pool: cp.Pool, Fetcher: func(hash node.Hash, _ string) (netutil.OutboundHTTPResult, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				entry, _ := cp.Pool.GetEntry(hash)
				return netutil.HTTPGetViaOutbound(ctx, *entry.Outbound.Load(), target.URL, netutil.OutboundHTTPOptions{})
			}})
			defer cp.ProbeMgr.Stop()
			check := func(allocated bool) {
				t.Helper()
				r := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes", nil, true)
				if r.Code != http.StatusOK {
					t.Fatal(r.Body.String())
				}
				rows := decodeJSONMap(t, r)["items"].([]any)
				if len(rows) != 2 {
					t.Fatalf("unexpected node count: %d", len(rows))
				}
				for _, value := range rows {
					row := value.(map[string]any)
					isLanding := row["node_hash"] == landing.Hex()
					if row["chain_proxy"] != isLanding {
						t.Fatalf("incorrect chain role: %+v", row)
					}
					if isLanding && allocated {
						if row["relay_node_hash"] != relay.Hex() || row["relay_node_tag"] != relaySub.Name()+"/Jump" {
							t.Fatalf("incorrect relay: %+v", row)
						}
					} else if row["relay_node_hash"] != nil || row["relay_node_tag"] != nil {
						t.Fatalf("unexpected relay allocation: %+v", row)
					}
				}
				detail := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes/"+landing.Hex(), nil, true)
				if detail.Code != http.StatusOK {
					t.Fatal(detail.Body.String())
				}
				row := decodeJSONMap(t, detail)
				if row["chain_proxy"] != true || (row["relay_node_hash"] == relay.Hex()) != allocated {
					t.Fatalf("node detail disagreed with allocation: %+v", row)
				}
			}
			check(false)
			for _, success := range []bool{true, false} {
				if !success {
					// The relay stays selected even if the landing's probe request fails.
					target.Close()
				}
				result := doJSONRequest(t, srv, http.MethodPost, "/api/v1/nodes/"+landing.Hex()+"/actions/probe-"+kind, nil, true)
				if (result.Code == http.StatusOK) != success {
					t.Fatalf("probe status=%d body=%s", result.Code, result.Body.String())
				}
				check(true)
			}
			relaySub.SetName("Renamed")
			check(true)
			relaySub.SetEnabled(false)
			check(false)
		})
	}
}
