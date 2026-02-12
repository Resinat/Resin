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
	"github.com/resin-proxy/resin/internal/service"
)

func main() {
	// 1. Load and validate environment config
	envCfg, err := config.LoadEnvConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}

	// 2. Create default runtime config
	runtimeCfg := config.NewDefaultRuntimeConfig()

	// 3. Wire services
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

	// 4. Create and start API server
	srv := api.NewServer(envCfg.APIPort, envCfg.AdminToken, systemSvc)

	go func() {
		log.Printf("Resin API server starting on :%d", envCfg.APIPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("API server error: %v", err)
		}
	}()

	// 5. Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("Received signal %s, shutting down...", sig)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Println("Server stopped")
}
