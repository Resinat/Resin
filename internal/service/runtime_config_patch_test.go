package service

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/proxy"
	"github.com/Resinat/Resin/internal/state"
)

type patchHarness struct {
	cp         *ControlPlaneService
	engine     *state.StateEngine
	runtimeCfg *atomic.Pointer[config.RuntimeConfig]
	stateDir   string
	cacheDir   string
	closeDB    func()
}

func newPatchHarness(t *testing.T) patchHarness {
	t.Helper()

	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	cacheDir := filepath.Join(root, "cache")

	engine, closer, err := state.PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}

	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	runtimeCfg.Store(config.NewDefaultRuntimeConfig())

	h := patchHarness{
		cp: &ControlPlaneService{
			Engine:     engine,
			RuntimeCfg: runtimeCfg,
		},
		engine:     engine,
		runtimeCfg: runtimeCfg,
		stateDir:   stateDir,
		cacheDir:   cacheDir,
		closeDB: func() {
			_ = closer.Close()
		},
	}
	t.Cleanup(h.closeDB)
	return h
}

func cloneRuntimeConfig(cfg *config.RuntimeConfig) *config.RuntimeConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.LatencyAuthorities = append([]string(nil), cfg.LatencyAuthorities...)
	return &out
}

func TestPatchRuntimeConfig_HotUpdatePersistsAndSurvivesRestart(t *testing.T) {
	h := newPatchHarness(t)

	patch := map[string]any{
		"request_log_enabled":                     true,
		"reverse_proxy_log_req_headers_max_bytes": 2048,
		"p2c_latency_window":                      "7m",
		"cache_flush_interval":                    "30s",
	}
	body, err := json.Marshal(patch)
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}

	updated, err := h.cp.PatchRuntimeConfig(body)
	if err != nil {
		t.Fatalf("PatchRuntimeConfig: %v", err)
	}

	if !updated.RequestLogEnabled {
		t.Fatal("request_log_enabled should be true after patch")
	}
	if updated.ReverseProxyLogReqHeadersMaxBytes != 2048 {
		t.Fatalf("reverse_proxy_log_req_headers_max_bytes=%d, want 2048", updated.ReverseProxyLogReqHeadersMaxBytes)
	}
	if time.Duration(updated.P2CLatencyWindow) != 7*time.Minute {
		t.Fatalf("p2c_latency_window=%v, want 7m", time.Duration(updated.P2CLatencyWindow))
	}
	if time.Duration(updated.CacheFlushInterval) != 30*time.Second {
		t.Fatalf("cache_flush_interval=%v, want 30s", time.Duration(updated.CacheFlushInterval))
	}

	live := h.runtimeCfg.Load()
	if !live.RequestLogEnabled ||
		live.ReverseProxyLogReqHeadersMaxBytes != 2048 ||
		time.Duration(live.P2CLatencyWindow) != 7*time.Minute ||
		time.Duration(live.CacheFlushInterval) != 30*time.Second {
		t.Fatalf("runtime atomic pointer not updated: %+v", live)
	}

	persisted, ver, err := h.engine.GetSystemConfig()
	if err != nil {
		t.Fatalf("GetSystemConfig: %v", err)
	}
	if ver != 1 {
		t.Fatalf("persisted version=%d, want 1", ver)
	}
	if !persisted.RequestLogEnabled ||
		persisted.ReverseProxyLogReqHeadersMaxBytes != 2048 ||
		time.Duration(persisted.P2CLatencyWindow) != 7*time.Minute ||
		time.Duration(persisted.CacheFlushInterval) != 30*time.Second {
		t.Fatalf("persisted config not updated: %+v", persisted)
	}

	// Simulate process restart: reopen state/cache and verify persisted values.
	h.closeDB()
	engine2, closer2, err := state.PersistenceBootstrap(h.stateDir, h.cacheDir)
	if err != nil {
		t.Fatalf("PersistenceBootstrap (restart): %v", err)
	}
	defer closer2.Close()

	afterRestart, _, err := engine2.GetSystemConfig()
	if err != nil {
		t.Fatalf("GetSystemConfig (restart): %v", err)
	}
	if !afterRestart.RequestLogEnabled ||
		afterRestart.ReverseProxyLogReqHeadersMaxBytes != 2048 ||
		time.Duration(afterRestart.P2CLatencyWindow) != 7*time.Minute ||
		time.Duration(afterRestart.CacheFlushInterval) != 30*time.Second {
		t.Fatalf("restart did not preserve patched config: %+v", afterRestart)
	}
}

func TestPatchRuntimeConfig_InvalidPatchDoesNotPartiallyApply(t *testing.T) {
	h := newPatchHarness(t)

	original := cloneRuntimeConfig(h.runtimeCfg.Load())
	if err := h.engine.SaveSystemConfig(original, 7, time.Now().UnixNano()); err != nil {
		t.Fatalf("SaveSystemConfig seed: %v", err)
	}

	_, err := h.cp.PatchRuntimeConfig([]byte(`{"latency_test_url":"not a url"}`))
	if err == nil {
		t.Fatal("expected validation error for invalid latency_test_url")
	}

	after := cloneRuntimeConfig(h.runtimeCfg.Load())
	if !reflect.DeepEqual(after, original) {
		t.Fatalf("in-memory config changed on invalid patch\nbefore=%+v\nafter=%+v", original, after)
	}

	persisted, ver, err := h.engine.GetSystemConfig()
	if err != nil {
		t.Fatalf("GetSystemConfig: %v", err)
	}
	if ver != 7 {
		t.Fatalf("version changed on invalid patch: got %d, want 7", ver)
	}
	if !reflect.DeepEqual(persisted, original) {
		t.Fatalf("persisted config changed on invalid patch\nbefore=%+v\nafter=%+v", original, persisted)
	}
}

func TestPatchRuntimeConfig_PersistFailureDoesNotSwapAtomicPointer(t *testing.T) {
	h := newPatchHarness(t)

	before := cloneRuntimeConfig(h.runtimeCfg.Load())

	// Close DB to force persistence path failure.
	h.closeDB()

	_, err := h.cp.PatchRuntimeConfig([]byte(`{"request_log_enabled":true}`))
	if err == nil {
		t.Fatal("expected persistence error after db close")
	}

	after := cloneRuntimeConfig(h.runtimeCfg.Load())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("atomic pointer swapped despite persist failure\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestPatchRuntimeConfig_ConcurrentPatchesNoLostUpdate(t *testing.T) {
	h := newPatchHarness(t)

	patches := [][]byte{
		[]byte(`{"request_log_enabled":true}`),
		[]byte(`{"cache_flush_interval":"45s"}`),
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(patches))
	start := make(chan struct{})

	for _, patch := range patches {
		wg.Add(1)
		go func(p []byte) {
			defer wg.Done()
			<-start
			_, err := h.cp.PatchRuntimeConfig(p)
			errCh <- err
		}(patch)
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent PatchRuntimeConfig error: %v", err)
		}
	}

	final := h.runtimeCfg.Load()
	if !final.RequestLogEnabled {
		t.Fatal("request_log_enabled lost after concurrent patch")
	}
	if time.Duration(final.CacheFlushInterval) != 45*time.Second {
		t.Fatalf("cache_flush_interval lost after concurrent patch: %v", time.Duration(final.CacheFlushInterval))
	}
}

func TestPatchRuntimeConfig_DoesNotMutateOldSliceSnapshot(t *testing.T) {
	h := newPatchHarness(t)

	before := h.runtimeCfg.Load()
	beforeAuthorities := append([]string(nil), before.LatencyAuthorities...)

	_, err := h.cp.PatchRuntimeConfig([]byte(`{"latency_authorities":["example.com","cloudflare.com"]}`))
	if err != nil {
		t.Fatalf("PatchRuntimeConfig: %v", err)
	}

	after := h.runtimeCfg.Load()
	if after == before {
		t.Fatal("expected atomic pointer to publish a new RuntimeConfig object")
	}

	if !reflect.DeepEqual(before.LatencyAuthorities, beforeAuthorities) {
		t.Fatalf("old snapshot latency_authorities mutated\nbefore=%v\nnow=%v", beforeAuthorities, before.LatencyAuthorities)
	}
	if reflect.DeepEqual(after.LatencyAuthorities, beforeAuthorities) {
		t.Fatalf("new snapshot latency_authorities did not apply patch: %v", after.LatencyAuthorities)
	}
}

type logCaptureEmitter struct {
	logs int
	last proxy.RequestLogEntry
}

func (e *logCaptureEmitter) EmitRequestFinished(proxy.RequestFinishedEvent) {}

func (e *logCaptureEmitter) EmitRequestLog(entry proxy.RequestLogEntry) {
	e.logs++
	e.last = entry
}

func TestPatchRuntimeConfig_DistributesToConfigAwareEventEmitter(t *testing.T) {
	h := newPatchHarness(t)

	base := &logCaptureEmitter{}
	emitter := proxy.ConfigAwareEventEmitter{
		Base: base,
		RequestLogEnabled: func() bool {
			return h.runtimeCfg.Load().RequestLogEnabled
		},
		ReverseProxyLogDetailEnabled: func() bool {
			return h.runtimeCfg.Load().ReverseProxyLogDetailEnabled
		},
		ReverseProxyLogReqHeadersMaxBytes: func() int {
			return h.runtimeCfg.Load().ReverseProxyLogReqHeadersMaxBytes
		},
		ReverseProxyLogReqBodyMaxBytes: func() int {
			return h.runtimeCfg.Load().ReverseProxyLogReqBodyMaxBytes
		},
		ReverseProxyLogRespHeadersMaxBytes: func() int {
			return h.runtimeCfg.Load().ReverseProxyLogRespHeadersMaxBytes
		},
		ReverseProxyLogRespBodyMaxBytes: func() int {
			return h.runtimeCfg.Load().ReverseProxyLogRespBodyMaxBytes
		},
	}

	// Default config has request_log_enabled=false, so no event should pass through.
	emitter.EmitRequestLog(proxy.RequestLogEntry{
		ProxyType:   proxy.ProxyTypeReverse,
		ReqHeaders:  []byte("0123456789"),
		ReqBody:     []byte("abcdef"),
		RespHeaders: []byte("zyxwvutsrq"),
		RespBody:    []byte("ok!"),
	})
	if base.logs != 0 {
		t.Fatalf("logs before enabling request_log = %d, want 0", base.logs)
	}

	_, err := h.cp.PatchRuntimeConfig([]byte(`{
		"request_log_enabled": true,
		"reverse_proxy_log_detail_enabled": true,
		"reverse_proxy_log_req_headers_max_bytes": 3,
		"reverse_proxy_log_req_body_max_bytes": 1,
		"reverse_proxy_log_resp_headers_max_bytes": 1,
		"reverse_proxy_log_resp_body_max_bytes": 1
	}`))
	if err != nil {
		t.Fatalf("PatchRuntimeConfig(enable+threshold): %v", err)
	}

	emitter.EmitRequestLog(proxy.RequestLogEntry{
		ProxyType:   proxy.ProxyTypeReverse,
		ReqHeaders:  []byte("0123456789"),
		ReqBody:     []byte("abcdef"),
		RespHeaders: []byte("zyxwvutsrq"),
		RespBody:    []byte("ok!"),
	})
	if base.logs != 1 {
		t.Fatalf("logs after enabling request_log = %d, want 1", base.logs)
	}
	if base.last.ReqHeadersLen != 10 {
		t.Fatalf("ReqHeadersLen = %d, want 10", base.last.ReqHeadersLen)
	}
	if !base.last.ReqHeadersTruncated {
		t.Fatal("ReqHeadersTruncated = false, want true")
	}
	if string(base.last.ReqHeaders) != "012" {
		t.Fatalf("ReqHeaders = %q, want %q", string(base.last.ReqHeaders), "012")
	}
	if string(base.last.ReqBody) != "a" || base.last.ReqBodyLen != 6 || !base.last.ReqBodyTruncated {
		t.Fatalf("ReqBody=%q len=%d truncated=%v, want %q len=6 truncated=true",
			string(base.last.ReqBody), base.last.ReqBodyLen, base.last.ReqBodyTruncated, "a")
	}
	if string(base.last.RespHeaders) != "z" || base.last.RespHeadersLen != 10 || !base.last.RespHeadersTruncated {
		t.Fatalf("RespHeaders=%q len=%d truncated=%v, want %q len=10 truncated=true",
			string(base.last.RespHeaders), base.last.RespHeadersLen, base.last.RespHeadersTruncated, "z")
	}
	if string(base.last.RespBody) != "o" || base.last.RespBodyLen != 3 || !base.last.RespBodyTruncated {
		t.Fatalf("RespBody=%q len=%d truncated=%v, want %q len=3 truncated=true",
			string(base.last.RespBody), base.last.RespBodyLen, base.last.RespBodyTruncated, "o")
	}

	_, err = h.cp.PatchRuntimeConfig([]byte(`{
		"reverse_proxy_log_req_headers_max_bytes": 12,
		"reverse_proxy_log_req_body_max_bytes": 12,
		"reverse_proxy_log_resp_headers_max_bytes": 12,
		"reverse_proxy_log_resp_body_max_bytes": 12
	}`))
	if err != nil {
		t.Fatalf("PatchRuntimeConfig(threshold up): %v", err)
	}

	emitter.EmitRequestLog(proxy.RequestLogEntry{
		ProxyType:   proxy.ProxyTypeReverse,
		ReqHeaders:  []byte("0123456789"),
		ReqBody:     []byte("abcdef"),
		RespHeaders: []byte("zyxwvutsrq"),
		RespBody:    []byte("ok!"),
	})
	if base.logs != 2 {
		t.Fatalf("logs after threshold up = %d, want 2", base.logs)
	}
	if base.last.ReqHeadersTruncated {
		t.Fatal("ReqHeadersTruncated after threshold up = true, want false")
	}
	if string(base.last.ReqHeaders) != "0123456789" {
		t.Fatalf("ReqHeaders after threshold up = %q, want %q", string(base.last.ReqHeaders), "0123456789")
	}
	if base.last.ReqBodyTruncated || string(base.last.ReqBody) != "abcdef" {
		t.Fatalf("ReqBody after threshold up = %q, truncated=%v", string(base.last.ReqBody), base.last.ReqBodyTruncated)
	}
	if base.last.RespHeadersTruncated || string(base.last.RespHeaders) != "zyxwvutsrq" {
		t.Fatalf("RespHeaders after threshold up = %q, truncated=%v", string(base.last.RespHeaders), base.last.RespHeadersTruncated)
	}
	if base.last.RespBodyTruncated || string(base.last.RespBody) != "ok!" {
		t.Fatalf("RespBody after threshold up = %q, truncated=%v", string(base.last.RespBody), base.last.RespBodyTruncated)
	}
}

func TestPatchRuntimeConfig_LatencyTestURLAutoAddsAuthority(t *testing.T) {
	h := newPatchHarness(t)

	updated, err := h.cp.PatchRuntimeConfig([]byte(`{
		"latency_authorities": ["cloudflare.com"],
		"latency_test_url": "https://www.gstatic.com/generate_204"
	}`))
	if err != nil {
		t.Fatalf("PatchRuntimeConfig: %v", err)
	}

	found := false
	for _, authority := range updated.LatencyAuthorities {
		if authority == "gstatic.com" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected gstatic.com to be auto-added, got %v", updated.LatencyAuthorities)
	}
}
