package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/state"
)

// ------------------------------------------------------------------
// Platform
// ------------------------------------------------------------------

// PlatformResponse is the API response model for a platform.
type PlatformResponse struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	StickyTTL              string   `json:"sticky_ttl"`
	RegexFilters           []string `json:"regex_filters"`
	RegionFilters          []string `json:"region_filters"`
	ReverseProxyMissAction string   `json:"reverse_proxy_miss_action"`
	AllocationPolicy       string   `json:"allocation_policy"`
	UpdatedAt              string   `json:"updated_at"`
}

func platformToResponse(p model.Platform) (PlatformResponse, error) {
	return PlatformResponse{
		ID:                     p.ID,
		Name:                   p.Name,
		StickyTTL:              time.Duration(p.StickyTTLNs).String(),
		RegexFilters:           append([]string(nil), p.RegexFilters...),
		RegionFilters:          append([]string(nil), p.RegionFilters...),
		ReverseProxyMissAction: p.ReverseProxyMissAction,
		AllocationPolicy:       p.AllocationPolicy,
		UpdatedAt:              time.Unix(0, p.UpdatedAtNs).UTC().Format(time.RFC3339Nano),
	}, nil
}

type platformConfig struct {
	Name                   string
	StickyTTLNs            int64
	RegexFilters           []string
	RegionFilters          []string
	ReverseProxyMissAction string
	AllocationPolicy       string
}

func (s *ControlPlaneService) defaultPlatformConfig(name string) platformConfig {
	return platformConfig{
		Name:                   name,
		StickyTTLNs:            int64(s.EnvCfg.DefaultPlatformStickyTTL),
		RegexFilters:           append([]string(nil), s.EnvCfg.DefaultPlatformRegexFilters...),
		RegionFilters:          append([]string(nil), s.EnvCfg.DefaultPlatformRegionFilters...),
		ReverseProxyMissAction: s.EnvCfg.DefaultPlatformReverseProxyMissAction,
		AllocationPolicy:       s.EnvCfg.DefaultPlatformAllocationPolicy,
	}
}

func platformConfigFromModel(mp model.Platform) (platformConfig, error) {
	return platformConfig{
		Name:                   mp.Name,
		StickyTTLNs:            mp.StickyTTLNs,
		RegexFilters:           append([]string(nil), mp.RegexFilters...),
		RegionFilters:          append([]string(nil), mp.RegionFilters...),
		ReverseProxyMissAction: mp.ReverseProxyMissAction,
		AllocationPolicy:       mp.AllocationPolicy,
	}, nil
}

func (cfg platformConfig) toModel(id string, updatedAtNs int64) model.Platform {
	return model.Platform{
		ID:                     id,
		Name:                   cfg.Name,
		StickyTTLNs:            cfg.StickyTTLNs,
		RegexFilters:           append([]string(nil), cfg.RegexFilters...),
		RegionFilters:          append([]string(nil), cfg.RegionFilters...),
		ReverseProxyMissAction: cfg.ReverseProxyMissAction,
		AllocationPolicy:       cfg.AllocationPolicy,
		UpdatedAtNs:            updatedAtNs,
	}
}

func (cfg platformConfig) toRuntime(id string) (*platform.Platform, error) {
	compiledRegexFilters, err := platform.CompileRegexFilters(cfg.RegexFilters)
	if err != nil {
		return nil, err
	}
	return platform.NewConfiguredPlatform(
		id,
		cfg.Name,
		compiledRegexFilters,
		cfg.RegionFilters,
		cfg.StickyTTLNs,
		cfg.ReverseProxyMissAction,
		cfg.AllocationPolicy,
	), nil
}

func validatePlatformMissAction(raw string) *ServiceError {
	if platform.ReverseProxyMissAction(raw).IsValid() {
		return nil
	}
	return invalidArg(fmt.Sprintf(
		"reverse_proxy_miss_action: must be %s or %s",
		platform.ReverseProxyMissActionRandom,
		platform.ReverseProxyMissActionReject,
	))
}

func validatePlatformAllocationPolicy(raw string) *ServiceError {
	if platform.AllocationPolicy(raw).IsValid() {
		return nil
	}
	return invalidArg(fmt.Sprintf(
		"allocation_policy: must be %s, %s, or %s",
		platform.AllocationPolicyBalanced,
		platform.AllocationPolicyPreferLowLatency,
		platform.AllocationPolicyPreferIdleIP,
	))
}

// ListPlatforms returns all platforms from the database.
func (s *ControlPlaneService) ListPlatforms() ([]PlatformResponse, error) {
	platforms, err := s.Engine.ListPlatforms()
	if err != nil {
		return nil, internal("list platforms", err)
	}
	resp := make([]PlatformResponse, len(platforms))
	for i, p := range platforms {
		platformResp, err := platformToResponse(p)
		if err != nil {
			return nil, internal(fmt.Sprintf("decode platform %s", p.ID), err)
		}
		resp[i] = platformResp
	}
	return resp, nil
}

func (s *ControlPlaneService) getPlatformModel(id string) (*model.Platform, error) {
	p, err := s.Engine.GetPlatform(id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, notFound("platform not found")
		}
		return nil, internal("get platform", err)
	}
	return p, nil
}

// GetPlatform returns a single platform by ID.
func (s *ControlPlaneService) GetPlatform(id string) (*PlatformResponse, error) {
	mp, err := s.getPlatformModel(id)
	if err != nil {
		return nil, err
	}
	r, err := platformToResponse(*mp)
	if err != nil {
		return nil, internal(fmt.Sprintf("decode platform %s", mp.ID), err)
	}
	return &r, nil
}

// CreatePlatformRequest holds create platform parameters.
type CreatePlatformRequest struct {
	Name                   *string  `json:"name"`
	StickyTTL              *string  `json:"sticky_ttl"`
	RegexFilters           []string `json:"regex_filters"`
	RegionFilters          []string `json:"region_filters"`
	ReverseProxyMissAction *string  `json:"reverse_proxy_miss_action"`
	AllocationPolicy       *string  `json:"allocation_policy"`
}

// CreatePlatform creates a new platform.
func (s *ControlPlaneService) CreatePlatform(req CreatePlatformRequest) (*PlatformResponse, error) {
	// Validate name.
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		return nil, invalidArg("name is required")
	}
	name := strings.TrimSpace(*req.Name)
	if name == platform.DefaultPlatformName {
		return nil, conflict("cannot use reserved name 'Default'")
	}

	// Apply defaults from env and overlay request fields.
	cfg := s.defaultPlatformConfig(name)
	if req.StickyTTL != nil {
		d, err := time.ParseDuration(*req.StickyTTL)
		if err != nil {
			return nil, invalidArg("sticky_ttl: " + err.Error())
		}
		if d <= 0 {
			return nil, invalidArg("sticky_ttl: must be > 0")
		}
		cfg.StickyTTLNs = int64(d)
	}
	if req.RegexFilters != nil {
		cfg.RegexFilters = req.RegexFilters
	}
	if req.RegionFilters != nil {
		cfg.RegionFilters = req.RegionFilters
	}
	if req.ReverseProxyMissAction != nil {
		if err := validatePlatformMissAction(*req.ReverseProxyMissAction); err != nil {
			return nil, err
		}
		cfg.ReverseProxyMissAction = *req.ReverseProxyMissAction
	}
	if req.AllocationPolicy != nil {
		if err := validatePlatformAllocationPolicy(*req.AllocationPolicy); err != nil {
			return nil, err
		}
		cfg.AllocationPolicy = *req.AllocationPolicy
	}
	if err := platform.ValidateRegionFilters(cfg.RegionFilters); err != nil {
		return nil, invalidArg(err.Error())
	}

	id := uuid.New().String()
	now := time.Now().UnixNano()
	plat, err := cfg.toRuntime(id)
	if err != nil {
		return nil, invalidArg(err.Error())
	}
	mp := cfg.toModel(id, now)
	if err := s.Engine.UpsertPlatform(mp); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return nil, conflict("platform name already exists")
		}
		return nil, internal("persist platform", err)
	}

	// Register in topology pool.
	// Build the routable view before publish so concurrent readers don't observe
	// a newly created platform with an empty view.
	s.Pool.RebuildPlatform(plat)
	s.Pool.RegisterPlatform(plat)

	r, err := platformToResponse(mp)
	if err != nil {
		return nil, internal(fmt.Sprintf("decode platform %s", mp.ID), err)
	}
	return &r, nil
}

// UpdatePlatform applies a constrained partial patch to a platform.
// This is not RFC 7396 JSON Merge Patch: patch must be a non-empty object and
// null values are rejected.
func (s *ControlPlaneService) UpdatePlatform(id string, patchJSON json.RawMessage) (*PlatformResponse, error) {
	patch, verr := parseMergePatch(patchJSON)
	if verr != nil {
		return nil, verr
	}
	if err := patch.validateFields(platformPatchAllowedFields, func(key string) string {
		return fmt.Sprintf("field %q is read-only or unknown", key)
	}); err != nil {
		return nil, err
	}

	// Load current.
	current, err := s.getPlatformModel(id)
	if err != nil {
		return nil, err
	}
	if current.ID == platform.DefaultPlatformID {
		if nameVal, ok := patch["name"]; ok {
			nameStr, _ := nameVal.(string)
			if nameStr != platform.DefaultPlatformName {
				return nil, conflict("cannot rename Default platform")
			}
		}
	}

	cfg, err := platformConfigFromModel(*current)
	if err != nil {
		return nil, err
	}

	// Apply patch to current config.
	if nameStr, ok, err := patch.optionalNonEmptyString("name"); err != nil {
		return nil, err
	} else if ok {
		cfg.Name = nameStr
		if cfg.Name == platform.DefaultPlatformName && current.ID != platform.DefaultPlatformID {
			return nil, conflict("cannot use reserved name 'Default'")
		}
	}

	if d, ok, err := patch.optionalDurationString("sticky_ttl"); err != nil {
		return nil, err
	} else if ok {
		if d <= 0 {
			return nil, invalidArg("sticky_ttl: must be > 0")
		}
		cfg.StickyTTLNs = int64(d)
	}

	if filters, ok, err := patch.optionalStringSlice("regex_filters"); err != nil {
		return nil, err
	} else if ok {
		cfg.RegexFilters = filters
	}

	regionFiltersPatched := false
	if filters, ok, err := patch.optionalStringSlice("region_filters"); err != nil {
		return nil, err
	} else if ok {
		regionFiltersPatched = true
		cfg.RegionFilters = filters
	}

	if v, ok := patch["reverse_proxy_miss_action"]; ok {
		ma, ok := v.(string)
		if !ok {
			return nil, invalidArg("reverse_proxy_miss_action: must be a string")
		}
		if err := validatePlatformMissAction(ma); err != nil {
			return nil, err
		}
		cfg.ReverseProxyMissAction = ma
	}

	if v, ok := patch["allocation_policy"]; ok {
		ap, ok := v.(string)
		if !ok {
			return nil, invalidArg(fmt.Sprintf(
				"allocation_policy: must be %s, %s, or %s",
				platform.AllocationPolicyBalanced,
				platform.AllocationPolicyPreferLowLatency,
				platform.AllocationPolicyPreferIdleIP,
			))
		}
		if err := validatePlatformAllocationPolicy(ap); err != nil {
			return nil, err
		}
		cfg.AllocationPolicy = ap
	}
	if regionFiltersPatched {
		if err := platform.ValidateRegionFilters(cfg.RegionFilters); err != nil {
			return nil, invalidArg(err.Error())
		}
	}

	plat, err := cfg.toRuntime(id)
	if err != nil {
		return nil, invalidArg(err.Error())
	}
	mp := cfg.toModel(id, time.Now().UnixNano())
	if err := s.Engine.UpsertPlatform(mp); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return nil, conflict("platform name already exists")
		}
		return nil, internal("persist platform", err)
	}

	// Replace in topology pool.
	if err := s.Pool.ReplacePlatform(plat); err != nil {
		return nil, internal("replace platform in pool", err)
	}

	r, err := platformToResponse(mp)
	if err != nil {
		return nil, internal(fmt.Sprintf("decode platform %s", mp.ID), err)
	}
	return &r, nil
}

// DeletePlatform deletes a platform.
func (s *ControlPlaneService) DeletePlatform(id string) error {
	if id == platform.DefaultPlatformID {
		return conflict("cannot delete Default platform")
	}

	if err := s.Engine.DeletePlatform(id); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return notFound("platform not found")
		}
		return internal("delete platform", err)
	}
	s.Pool.UnregisterPlatform(id)
	return nil
}

// ResetPlatformToDefault resets a platform to env defaults.
func (s *ControlPlaneService) ResetPlatformToDefault(id string) (*PlatformResponse, error) {
	if id == platform.DefaultPlatformID {
		return nil, conflict("cannot reset Default platform to defaults")
	}

	name, err := s.Engine.GetPlatformName(id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, notFound("platform not found")
		}
		return nil, internal("get platform", err)
	}

	cfg := s.defaultPlatformConfig(name)
	plat, err := cfg.toRuntime(id)
	if err != nil {
		// Environment defaults should be validated on process startup.
		return nil, internal("compile default platform regex filters", err)
	}
	mp := cfg.toModel(id, time.Now().UnixNano())
	if err := s.Engine.UpsertPlatform(mp); err != nil {
		return nil, internal("persist platform", err)
	}

	if err := s.Pool.ReplacePlatform(plat); err != nil {
		return nil, internal("replace platform in pool", err)
	}

	r, err := platformToResponse(mp)
	if err != nil {
		return nil, internal(fmt.Sprintf("decode platform %s", mp.ID), err)
	}
	return &r, nil
}

// RebuildPlatformView triggers a full rebuild of the platform's routable view.
func (s *ControlPlaneService) RebuildPlatformView(id string) error {
	plat, ok := s.Pool.GetPlatform(id)
	if !ok {
		return notFound("platform not found")
	}
	s.Pool.RebuildPlatform(plat)
	return nil
}

// PreviewFilterRequest holds preview filter parameters.
type PreviewFilterRequest struct {
	PlatformID   *string             `json:"platform_id"`
	PlatformSpec *PlatformSpecFilter `json:"platform_spec"`
}

type PlatformSpecFilter struct {
	RegexFilters  []string `json:"regex_filters"`
	RegionFilters []string `json:"region_filters"`
}

// NodeSummary is the API response for a node.
type NodeSummary struct {
	NodeHash         string    `json:"node_hash"`
	CreatedAt        string    `json:"created_at"`
	HasOutbound      bool      `json:"has_outbound"`
	LastError        string    `json:"last_error,omitempty"`
	CircuitOpenSince *string   `json:"circuit_open_since"`
	FailureCount     int       `json:"failure_count"`
	EgressIP         string    `json:"egress_ip,omitempty"`
	Region           string    `json:"region,omitempty"`
	LastEgressUpdate string    `json:"last_egress_update,omitempty"`
	Tags             []NodeTag `json:"tags"`
}

type NodeTag struct {
	SubscriptionID          string `json:"subscription_id"`
	SubscriptionName        string `json:"subscription_name"`
	Tag                     string `json:"tag"`
	SubscriptionCreatedAtNs int64  `json:"-"`
}

func (s *ControlPlaneService) nodeEntryToSummary(h node.Hash, entry *node.NodeEntry) NodeSummary {
	ns := NodeSummary{
		NodeHash:     h.Hex(),
		CreatedAt:    entry.CreatedAt.UTC().Format(time.RFC3339Nano),
		HasOutbound:  entry.HasOutbound(),
		LastError:    entry.GetLastError(),
		FailureCount: int(entry.FailureCount.Load()),
	}

	if cos := entry.CircuitOpenSince.Load(); cos > 0 {
		t := time.Unix(0, cos).UTC().Format(time.RFC3339Nano)
		ns.CircuitOpenSince = &t
	}

	egressIP := entry.GetEgressIP()
	if egressIP.IsValid() {
		ns.EgressIP = egressIP.String()
		ns.Region = s.GeoIP.Lookup(egressIP)
	}

	if leu := entry.LastEgressUpdate.Load(); leu > 0 {
		ns.LastEgressUpdate = time.Unix(0, leu).UTC().Format(time.RFC3339Nano)
	}

	// Build tags.
	subIDs := entry.SubscriptionIDs()
	for _, subID := range subIDs {
		sub := s.SubMgr.Lookup(subID)
		if sub == nil {
			continue
		}
		tags, ok := sub.ManagedNodes().Load(h)
		if !ok {
			continue
		}
		for _, tag := range tags {
			ns.Tags = append(ns.Tags, NodeTag{
				SubscriptionID:          subID,
				SubscriptionName:        sub.Name(),
				Tag:                     sub.Name() + "/" + tag,
				SubscriptionCreatedAtNs: sub.CreatedAtNs,
			})
		}
	}
	if ns.Tags == nil {
		ns.Tags = []NodeTag{}
	}
	return ns
}

// PreviewFilter returns nodes matching the given filter spec.
func (s *ControlPlaneService) PreviewFilter(req PreviewFilterRequest) ([]NodeSummary, error) {
	hasPlatformID := req.PlatformID != nil && *req.PlatformID != ""
	hasPlatformSpec := req.PlatformSpec != nil

	if hasPlatformID == hasPlatformSpec {
		return nil, invalidArg("exactly one of platform_id or platform_spec is required")
	}

	var regexFilters []*regexp.Regexp
	var regionFilters []string

	if hasPlatformID {
		plat, ok := s.Pool.GetPlatform(*req.PlatformID)
		if !ok {
			return nil, notFound("platform not found")
		}
		regexFilters = plat.RegexFilters
		regionFilters = plat.RegionFilters
	} else {
		compiled, err := platform.CompileRegexFilters(req.PlatformSpec.RegexFilters)
		if err != nil {
			return nil, invalidArg(err.Error())
		}
		regexFilters = compiled
		regionFilters = req.PlatformSpec.RegionFilters
		if err := platform.ValidateRegionFilters(regionFilters); err != nil {
			return nil, invalidArg(err.Error())
		}
	}

	var subLookup node.SubLookupFunc
	if len(regexFilters) > 0 {
		subLookup = s.buildSubLookup()
	}
	var regionFilterSet map[string]struct{}
	if len(regionFilters) > 0 {
		regionFilterSet = make(map[string]struct{}, len(regionFilters))
		for _, rf := range regionFilters {
			regionFilterSet[rf] = struct{}{}
		}
	}

	var result []NodeSummary
	s.Pool.Range(func(h node.Hash, entry *node.NodeEntry) bool {
		if len(regexFilters) > 0 && !entry.MatchRegexs(regexFilters, subLookup) {
			return true
		}
		if len(regionFilterSet) > 0 {
			egressIP := entry.GetEgressIP()
			if !egressIP.IsValid() {
				return true
			}
			region := s.GeoIP.Lookup(egressIP)
			if _, ok := regionFilterSet[region]; !ok {
				return true
			}
		}
		result = append(result, s.nodeEntryToSummary(h, entry))
		return true
	})
	return result, nil
}
