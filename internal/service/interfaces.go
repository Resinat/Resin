// Package service defines the interfaces that API handlers depend on.
// Concrete implementations live in other packages and are wired in main.
package service

import (
	"sync/atomic"
	"time"

	"github.com/resin-proxy/resin/internal/config"
)

// SystemInfo contains version and runtime information.
type SystemInfo struct {
	Version   string    `json:"version"`
	GitCommit string    `json:"git_commit"`
	BuildTime string    `json:"build_time"`
	StartedAt time.Time `json:"started_at"`
}

// SystemService provides system-level operations.
type SystemService interface {
	GetSystemInfo() SystemInfo
	GetRuntimeConfig() *config.RuntimeConfig
}

// --- Minimal in-memory implementation for Phase 1 ---

// MemorySystemService is a minimal SystemService backed by in-memory state.
type MemorySystemService struct {
	info       SystemInfo
	runtimeCfg *atomic.Pointer[config.RuntimeConfig]
}

// NewMemorySystemService creates a MemorySystemService with the given info and config.
func NewMemorySystemService(
	info SystemInfo,
	runtimeCfg *atomic.Pointer[config.RuntimeConfig],
) *MemorySystemService {
	return &MemorySystemService{
		info:       info,
		runtimeCfg: runtimeCfg,
	}
}

func (s *MemorySystemService) GetSystemInfo() SystemInfo {
	return s.info
}

func (s *MemorySystemService) GetRuntimeConfig() *config.RuntimeConfig {
	if s.runtimeCfg == nil {
		return nil
	}
	return s.runtimeCfg.Load()
}
