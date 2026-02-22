package main

import (
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
)

func TestDeriveRequestLogRuntimeSettings_FromEnv(t *testing.T) {
	envCfg := &config.EnvConfig{
		RequestLogQueueSize:           1234,
		RequestLogQueueFlushBatchSize: 321,
		RequestLogQueueFlushInterval:  7 * time.Second,
		RequestLogDBMaxMB:             64,
		RequestLogDBRetainCount:       9,
	}

	got := deriveRequestLogRuntimeSettings(envCfg)
	if got.QueueSize != 1234 {
		t.Fatalf("QueueSize: got %d, want %d", got.QueueSize, 1234)
	}
	if got.FlushBatch != 321 {
		t.Fatalf("FlushBatch: got %d, want %d", got.FlushBatch, 321)
	}
	if got.FlushInterval != 7*time.Second {
		t.Fatalf("FlushInterval: got %v, want %v", got.FlushInterval, 7*time.Second)
	}
	if got.DBMaxBytes != int64(64)*1024*1024 {
		t.Fatalf("DBMaxBytes: got %d, want %d", got.DBMaxBytes, int64(64)*1024*1024)
	}
	if got.DBRetainCount != 9 {
		t.Fatalf("DBRetainCount: got %d, want %d", got.DBRetainCount, 9)
	}
}

func TestDeriveMetricsManagerSettings_FromEnv(t *testing.T) {
	envCfg := &config.EnvConfig{
		MetricThroughputIntervalSeconds:   2,
		MetricThroughputRetentionSeconds:  100,
		MetricConnectionsIntervalSeconds:  5,
		MetricConnectionsRetentionSeconds: 18000,
		MetricLeasesIntervalSeconds:       10,
		MetricLeasesRetentionSeconds:      12000,
		MetricBucketSeconds:               3600,
		MetricLatencyBinWidthMS:           80,
		MetricLatencyBinOverflowMS:        2500,
	}

	got := deriveMetricsManagerSettings(envCfg)
	if got.ThroughputIntervalSec != 2 {
		t.Fatalf("ThroughputIntervalSec: got %d, want %d", got.ThroughputIntervalSec, 2)
	}
	if got.ThroughputRealtimeCapacity != 50 {
		t.Fatalf("ThroughputRealtimeCapacity: got %d, want %d", got.ThroughputRealtimeCapacity, 50)
	}
	if got.ConnectionsIntervalSec != 5 {
		t.Fatalf("ConnectionsIntervalSec: got %d, want %d", got.ConnectionsIntervalSec, 5)
	}
	if got.ConnectionsRealtimeCapacity != 3600 {
		t.Fatalf("ConnectionsRealtimeCapacity: got %d, want %d", got.ConnectionsRealtimeCapacity, 3600)
	}
	if got.LeasesIntervalSec != 10 {
		t.Fatalf("LeasesIntervalSec: got %d, want %d", got.LeasesIntervalSec, 10)
	}
	if got.LeasesRealtimeCapacity != 1200 {
		t.Fatalf("LeasesRealtimeCapacity: got %d, want %d", got.LeasesRealtimeCapacity, 1200)
	}
	if got.BucketSeconds != 3600 {
		t.Fatalf("BucketSeconds: got %d, want %d", got.BucketSeconds, 3600)
	}
	if got.LatencyBinMs != 80 {
		t.Fatalf("LatencyBinMs: got %d, want %d", got.LatencyBinMs, 80)
	}
	if got.LatencyOverflowMs != 2500 {
		t.Fatalf("LatencyOverflowMs: got %d, want %d", got.LatencyOverflowMs, 2500)
	}
}
