package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/resin-proxy/resin/internal/api"
	"github.com/resin-proxy/resin/internal/buildinfo"
	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/service"
	"github.com/resin-proxy/resin/internal/state"
	"github.com/resin-proxy/resin/internal/subscription"
	"github.com/resin-proxy/resin/internal/topology"
)

func main() {
	// 1. Load and validate environment config
	envCfg, err := config.LoadEnvConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}

	// 2. Initialize persistence (state.db + cache.db)
	engine, dbCloser, err := state.PersistenceBootstrap(envCfg.StateDir, envCfg.CacheDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: persistence bootstrap: %v\n", err)
		os.Exit(1)
	}
	defer dbCloser.Close()
	log.Println("Persistence bootstrap complete")

	// 3. Load runtime config from state.db; fall back to defaults if empty
	runtimeCfg, ver, err := engine.GetSystemConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: load system config: %v\n", err)
		os.Exit(1)
	}
	if runtimeCfg == nil {
		runtimeCfg = config.NewDefaultRuntimeConfig()
		log.Println("No persisted runtime config found, using defaults")
	} else {
		log.Printf("Loaded persisted runtime config (version %d)", ver)
	}

	// 4. Initialize Stage 3 topology components
	subManager := topology.NewSubscriptionManager()

	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup: subManager.Lookup,
		GeoLookup: func(addr netip.Addr) string {
			// Stub GeoIP — returns empty string until Stage 4 integration.
			return ""
		},
		OnNodeAdded: func(hash node.Hash) {
			engine.MarkNodeStatic(hash.Hex())
		},
		OnNodeRemoved: func(hash node.Hash) {
			engine.MarkNodeStaticDelete(hash.Hex())
		},
		OnSubNodeChanged: func(subID string, hash node.Hash, added bool) {
			if added {
				engine.MarkSubscriptionNode(subID, hash.Hex())
			} else {
				engine.MarkSubscriptionNodeDelete(subID, hash.Hex())
			}
		},
	})
	log.Println("Topology: GlobalNodePool initialized")

	scheduler := topology.NewSubscriptionScheduler(topology.SchedulerConfig{
		SubManager:  subManager,
		Pool:        pool,
		HTTPTimeout: 30 * time.Second,
		UserAgent:   "Resin/" + buildinfo.Version,
		// OnSubUpdated persistence callback — will wire to engine.UpsertSubscription
		// once subscription runtime model exposes model.Subscription conversion.
	})

	ephemeralCleaner := topology.NewEphemeralCleaner(
		subManager,
		pool,
		time.Duration(runtimeCfg.EphemeralNodeEvictDelay),
	)

	// 4b. Bootstrap: load subscriptions + platforms from state.db
	dbSubs, err := engine.ListSubscriptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: load subscriptions: %v\n", err)
		os.Exit(1)
	}
	for _, ms := range dbSubs {
		sub := subscription.NewSubscription(
			ms.ID, ms.Name, ms.URL, ms.Enabled, ms.Ephemeral,
		)
		sub.UpdateIntervalNs = ms.UpdateIntervalNs
		sub.CreatedAtNs = ms.CreatedAtNs
		sub.UpdatedAtNs = ms.UpdatedAtNs
		subManager.Register(sub)
	}
	log.Printf("Loaded %d subscriptions from state.db", len(dbSubs))

	dbPlats, err := engine.ListPlatforms()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: load platforms: %v\n", err)
		os.Exit(1)
	}
	for _, mp := range dbPlats {
		var regexStrs []string
		_ = json.Unmarshal([]byte(mp.RegexFiltersJSON), &regexStrs)
		var compiled []*regexp.Regexp
		for _, rs := range regexStrs {
			if re, err := regexp.Compile(rs); err == nil {
				compiled = append(compiled, re)
			}
		}
		var regionFilters []string
		_ = json.Unmarshal([]byte(mp.RegionFiltersJSON), &regionFilters)
		plat := platform.NewPlatform(mp.ID, mp.Name, compiled, regionFilters)
		plat.StickyTTLNs = mp.StickyTTLNs
		plat.ReverseProxyMissAction = mp.ReverseProxyMissAction
		plat.AllocationPolicy = mp.AllocationPolicy
		pool.RegisterPlatform(plat)
	}
	log.Printf("Loaded %d platforms from state.db", len(dbPlats))

	// 5. Start cache flush worker with topology-aware readers
	flushReaders := state.CacheReaders{
		ReadNodeStatic: func(hash string) *model.NodeStatic {
			h, err := node.ParseHex(hash)
			if err != nil {
				return nil
			}
			entry, ok := pool.GetEntry(h)
			if !ok {
				return nil
			}
			return &model.NodeStatic{
				Hash:           hash,
				RawOptionsJSON: string(entry.RawOptions),
				CreatedAtNs:    entry.CreatedAt.UnixNano(),
			}
		},
		ReadNodeDynamic: func(hash string) *model.NodeDynamic {
			h, err := node.ParseHex(hash)
			if err != nil {
				return nil
			}
			entry, ok := pool.GetEntry(h)
			if !ok {
				return nil
			}
			egressIP := entry.GetEgressIP()
			egressStr := ""
			if egressIP.IsValid() {
				egressStr = egressIP.String()
			}
			return &model.NodeDynamic{
				Hash:              hash,
				FailureCount:      int(entry.FailureCount.Load()),
				CircuitOpenSince:  entry.CircuitOpenSince.Load(),
				EgressIP:          egressStr,
				EgressUpdatedAtNs: entry.LastEgressUpdate.Load(),
			}
		},
		ReadNodeLatency: func(key model.NodeLatencyKey) *model.NodeLatency {
			// Latency records not yet managed in Stage 3.
			return nil
		},
		ReadLease: func(key model.LeaseKey) *model.Lease {
			// Leases not yet managed in Stage 3.
			return nil
		},
		ReadSubscriptionNode: func(key model.SubscriptionNodeKey) *model.SubscriptionNode {
			h, err := node.ParseHex(key.NodeHash)
			if err != nil {
				return nil
			}
			sub := subManager.Lookup(key.SubscriptionID)
			if sub == nil {
				return nil
			}
			tags, ok := sub.ManagedNodes().Load(h)
			if !ok {
				return nil
			}
			tagsJSONBytes, _ := json.Marshal(tags)
			tagsJSON := string(tagsJSONBytes)
			return &model.SubscriptionNode{
				SubscriptionID: key.SubscriptionID,
				NodeHash:       key.NodeHash,
				TagsJSON:       tagsJSON,
			}
		},
	}
	flushWorker := state.NewCacheFlushWorker(
		engine, flushReaders,
		runtimeCfg.CacheFlushDirtyThreshold,
		time.Duration(runtimeCfg.CacheFlushInterval),
		5*time.Second, // check tick
	)
	flushWorker.Start()
	log.Println("Cache flush worker started")

	// 6. Start topology background workers
	scheduler.Start()
	log.Println("Subscription scheduler started")

	ephemeralCleaner.Start()
	log.Println("Ephemeral cleaner started")

	// 7. Wire services
	startedAt := time.Now().UTC()
	systemSvc := service.NewMemorySystemService(
		service.SystemInfo{
			Version:   buildinfo.Version,
			GitCommit: buildinfo.GitCommit,
			BuildTime: buildinfo.BuildTime,
			StartedAt: startedAt,
		},
		runtimeCfg,
	)

	// 8. Create and start API server
	srv := api.NewServer(envCfg.APIPort, envCfg.AdminToken, systemSvc)

	go func() {
		log.Printf("Resin API server starting on :%d", envCfg.APIPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("API server error: %v", err)
		}
	}()

	// 9. Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("Received signal %s, shutting down...", sig)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	// Stop in reverse order: ephemeral cleaner → scheduler → flush worker.
	ephemeralCleaner.Stop()
	log.Println("Ephemeral cleaner stopped")

	scheduler.Stop()
	log.Println("Subscription scheduler stopped")

	flushWorker.Stop() // final flush before DB close
	log.Println("Server stopped")
}

