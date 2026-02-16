package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/config"
)

func TestMemorySystemService_RuntimeConfigUsesAtomicPointer(t *testing.T) {
	cfgPtr := &atomic.Pointer[config.RuntimeConfig]{}
	cfgA := config.NewDefaultRuntimeConfig()
	cfgA.UserAgent = "ua-a"
	cfgPtr.Store(cfgA)

	svc := NewMemorySystemService(SystemInfo{
		Version:   "v1",
		GitCommit: "c1",
		BuildTime: "2026-01-01T00:00:00Z",
		StartedAt: time.Unix(0, 0).UTC(),
	}, cfgPtr)

	gotA := svc.GetRuntimeConfig()
	if gotA == nil || gotA.UserAgent != "ua-a" {
		t.Fatalf("first load: got %+v", gotA)
	}

	cfgB := config.NewDefaultRuntimeConfig()
	cfgB.UserAgent = "ua-b"
	cfgPtr.Store(cfgB)

	gotB := svc.GetRuntimeConfig()
	if gotB == nil || gotB.UserAgent != "ua-b" {
		t.Fatalf("after swap: got %+v", gotB)
	}
}
