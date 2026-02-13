package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/resin-proxy/resin/internal/api"
	"github.com/resin-proxy/resin/internal/buildinfo"
	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/service"
	"github.com/resin-proxy/resin/internal/state"
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

	// 4. Start cache flush worker
	// TODO: replace placeholder readers with real in-memory store lookups
	//       when the runtime data layer is implemented (phase 3).
	flushReaders := state.CacheReaders{
		ReadNodeStatic:       func(string) *model.NodeStatic { return nil },
		ReadNodeDynamic:      func(string) *model.NodeDynamic { return nil },
		ReadNodeLatency:      func(model.NodeLatencyKey) *model.NodeLatency { return nil },
		ReadLease:            func(model.LeaseKey) *model.Lease { return nil },
		ReadSubscriptionNode: func(model.SubscriptionNodeKey) *model.SubscriptionNode { return nil },
	}
	flushWorker := state.NewCacheFlushWorker(
		engine, flushReaders,
		runtimeCfg.CacheFlushDirtyThreshold,
		time.Duration(runtimeCfg.CacheFlushInterval),
		5*time.Second, // check tick
	)
	flushWorker.Start()
	log.Println("Cache flush worker started")

	// 5. Wire services
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

	// 6. Create and start API server
	srv := api.NewServer(envCfg.APIPort, envCfg.AdminToken, systemSvc)

	go func() {
		log.Printf("Resin API server starting on :%d", envCfg.APIPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("API server error: %v", err)
		}
	}()

	// 7. Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("Received signal %s, shutting down...", sig)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	flushWorker.Stop() // final flush before DB close
	log.Println("Server stopped")
}
