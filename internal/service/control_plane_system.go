package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/geoip"
	"github.com/resin-proxy/resin/internal/netutil"
	"github.com/resin-proxy/resin/internal/probe"
	"github.com/resin-proxy/resin/internal/proxy"
	"github.com/resin-proxy/resin/internal/routing"
	"github.com/resin-proxy/resin/internal/state"
	"github.com/resin-proxy/resin/internal/topology"
)

// ServiceError wraps an error with a code for API response mapping.
type ServiceError struct {
	Code    string // INVALID_ARGUMENT, NOT_FOUND, CONFLICT, INTERNAL
	Message string
	Err     error
}

func (e *ServiceError) Error() string { return e.Message }
func (e *ServiceError) Unwrap() error { return e.Err }

func invalidArg(msg string) *ServiceError {
	return &ServiceError{Code: "INVALID_ARGUMENT", Message: msg}
}

func notFound(msg string) *ServiceError {
	return &ServiceError{Code: "NOT_FOUND", Message: msg}
}

func conflict(msg string) *ServiceError {
	return &ServiceError{Code: "CONFLICT", Message: msg}
}

func internal(msg string, err error) *ServiceError {
	return &ServiceError{Code: "INTERNAL", Message: msg, Err: err}
}

// --- ControlPlaneService ---

// ControlPlaneService provides all control plane operations.
// Handlers call its methods; business logic lives here, not in handlers.
type ControlPlaneService struct {
	Engine         *state.StateEngine
	Pool           *topology.GlobalNodePool
	SubMgr         *topology.SubscriptionManager
	Scheduler      *topology.SubscriptionScheduler
	Router         *routing.Router
	GeoIP          *geoip.Service
	ProbeMgr       *probe.ProbeManager
	MatcherRuntime *proxy.AccountMatcherRuntime
	RuntimeCfg     *atomic.Pointer[config.RuntimeConfig]
	EnvCfg         *config.EnvConfig

	configMu      sync.Mutex
	configVersion int
}

// ------------------------------------------------------------------
// System Config
// ------------------------------------------------------------------

// runtimeConfigAllowedFields is the set of JSON field names that can be patched.
var runtimeConfigAllowedFields = map[string]bool{
	"user_agent":                               true,
	"request_log_enabled":                      true,
	"reverse_proxy_log_detail_enabled":         true,
	"reverse_proxy_log_req_headers_max_bytes":  true,
	"reverse_proxy_log_req_body_max_bytes":     true,
	"reverse_proxy_log_resp_headers_max_bytes": true,
	"reverse_proxy_log_resp_body_max_bytes":    true,
	"max_consecutive_failures":                 true,
	"max_latency_test_interval":                true,
	"max_authority_latency_test_interval":      true,
	"max_egress_test_interval":                 true,
	"latency_test_url":                         true,
	"latency_authorities":                      true,
	"p2c_latency_window":                       true,
	"latency_decay_window":                     true,
	"cache_flush_interval":                     true,
	"cache_flush_dirty_threshold":              true,
	"ephemeral_node_evict_delay":               true,
}

var platformPatchAllowedFields = map[string]bool{
	"name":                      true,
	"sticky_ttl":                true,
	"regex_filters":             true,
	"region_filters":            true,
	"reverse_proxy_miss_action": true,
	"allocation_policy":         true,
}

var subscriptionPatchAllowedFields = map[string]bool{
	"name":            true,
	"url":             true,
	"update_interval": true,
	"enabled":         true,
	"ephemeral":       true,
}

func parseRuntimeConfigPatch(patchJSON json.RawMessage, out *config.RuntimeConfig) *ServiceError {
	var rawPatch map[string]json.RawMessage
	if err := json.Unmarshal(patchJSON, &rawPatch); err != nil {
		return invalidArg("invalid JSON: " + err.Error())
	}
	if len(rawPatch) == 0 {
		return invalidArg("empty patch")
	}
	for key, raw := range rawPatch {
		if !runtimeConfigAllowedFields[key] {
			return invalidArg(fmt.Sprintf("unknown or read-only field: %q", key))
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return invalidArg(fmt.Sprintf("null value not allowed for field: %q", key))
		}
	}

	dec := json.NewDecoder(bytes.NewReader(patchJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return invalidArg("validation failed: " + err.Error())
	}
	return nil
}

func copyRuntimeConfig(cfg *config.RuntimeConfig) *config.RuntimeConfig {
	if cfg == nil {
		return config.NewDefaultRuntimeConfig()
	}
	out := *cfg
	out.LatencyAuthorities = append([]string(nil), cfg.LatencyAuthorities...)
	return &out
}

// PatchRuntimeConfig applies a constrained partial patch to the runtime config.
// This is not RFC 7396 JSON Merge Patch: patch must be a non-empty object and
// null values are rejected.
// Pipeline: validate → persist → atomic swap.
func (s *ControlPlaneService) PatchRuntimeConfig(patchJSON json.RawMessage) (*config.RuntimeConfig, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	// 3. Deep-copy current config → apply patch.
	newCfg := copyRuntimeConfig(s.RuntimeCfg.Load())
	if verr := parseRuntimeConfigPatch(patchJSON, newCfg); verr != nil {
		return nil, verr
	}

	// 4. Additional validation.
	if err := validateRuntimeConfig(newCfg); err != nil {
		return nil, err
	}

	// On process start, initialize local configVersion from persisted state
	// so PATCH keeps monotonically increasing versions across restarts.
	if s.configVersion == 0 && s.Engine != nil {
		_, persistedVersion, err := s.Engine.GetSystemConfig()
		if err != nil {
			return nil, internal("load persisted config version", err)
		}
		if persistedVersion > s.configVersion {
			s.configVersion = persistedVersion
		}
	}

	// 5. Persist.
	newVersion := s.configVersion + 1
	if err := s.Engine.SaveSystemConfig(newCfg, newVersion, time.Now().UnixNano()); err != nil {
		return nil, internal("persist config", err)
	}

	// 6. Atomic swap.
	s.RuntimeCfg.Store(newCfg)
	s.configVersion = newVersion

	return newCfg, nil
}

func validateRuntimeConfig(cfg *config.RuntimeConfig) *ServiceError {
	latencyURL := strings.TrimSpace(cfg.LatencyTestURL)
	u, verr := parseHTTPAbsoluteURL("latency_test_url", latencyURL)
	if verr != nil {
		return verr
	}
	latencyDomain := strings.ToLower(netutil.ExtractDomain(u.Host))
	if cfg.MaxConsecutiveFailures < 0 {
		return invalidArg("max_consecutive_failures: must be non-negative")
	}
	if cfg.CacheFlushDirtyThreshold < 0 {
		return invalidArg("cache_flush_dirty_threshold: must be non-negative")
	}
	// Request log bytes fields must be non-negative.
	if cfg.ReverseProxyLogReqHeadersMaxBytes < 0 {
		return invalidArg("reverse_proxy_log_req_headers_max_bytes: must be non-negative")
	}
	if cfg.ReverseProxyLogReqBodyMaxBytes < 0 {
		return invalidArg("reverse_proxy_log_req_body_max_bytes: must be non-negative")
	}
	if cfg.ReverseProxyLogRespHeadersMaxBytes < 0 {
		return invalidArg("reverse_proxy_log_resp_headers_max_bytes: must be non-negative")
	}
	if cfg.ReverseProxyLogRespBodyMaxBytes < 0 {
		return invalidArg("reverse_proxy_log_resp_body_max_bytes: must be non-negative")
	}
	minProbeInterval := 30 * time.Second
	// Probe intervals must be at least 30s (DESIGN.md).
	if time.Duration(cfg.MaxLatencyTestInterval) < minProbeInterval {
		return invalidArg("max_latency_test_interval: must be >= 30s")
	}
	if time.Duration(cfg.MaxAuthorityLatencyTestInterval) < minProbeInterval {
		return invalidArg("max_authority_latency_test_interval: must be >= 30s")
	}
	if time.Duration(cfg.MaxEgressTestInterval) < minProbeInterval {
		return invalidArg("max_egress_test_interval: must be >= 30s")
	}
	if cfg.P2CLatencyWindow < 0 {
		return invalidArg("p2c_latency_window: must be non-negative")
	}
	if cfg.LatencyDecayWindow < 0 {
		return invalidArg("latency_decay_window: must be non-negative")
	}
	if cfg.CacheFlushInterval < 0 {
		return invalidArg("cache_flush_interval: must be non-negative")
	}
	if cfg.EphemeralNodeEvictDelay < 0 {
		return invalidArg("ephemeral_node_evict_delay: must be non-negative")
	}

	// LatencyTestURL domain must be in LatencyAuthorities.
	// If absent, append it instead of returning an error.
	if latencyDomain != "" {
		found := false
		for _, authority := range cfg.LatencyAuthorities {
			if strings.EqualFold(strings.TrimSpace(authority), latencyDomain) {
				found = true
				break
			}
		}
		if !found {
			cfg.LatencyAuthorities = append(cfg.LatencyAuthorities, latencyDomain)
		}
	}
	return nil
}
