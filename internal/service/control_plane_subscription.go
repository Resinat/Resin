package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/state"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
)

// ------------------------------------------------------------------
// Subscription
// ------------------------------------------------------------------

// SubscriptionResponse is the API response for a subscription.
type SubscriptionResponse struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	URL                     string `json:"url"`
	UpdateInterval          string `json:"update_interval"`
	NodeCount               int    `json:"node_count"`
	Ephemeral               bool   `json:"ephemeral"`
	EphemeralNodeEvictDelay string `json:"ephemeral_node_evict_delay"`
	Enabled                 bool   `json:"enabled"`
	CreatedAt               string `json:"created_at"`
	LastChecked             string `json:"last_checked,omitempty"`
	LastUpdated             string `json:"last_updated,omitempty"`
	LastError               string `json:"last_error,omitempty"`
}

func subToResponse(sub *subscription.Subscription) SubscriptionResponse {
	nodeCount := 0
	if managed := sub.ManagedNodes(); managed != nil {
		nodeCount = managed.Size()
	}

	resp := SubscriptionResponse{
		ID:                      sub.ID,
		Name:                    sub.Name(),
		URL:                     sub.URL(),
		UpdateInterval:          time.Duration(sub.UpdateIntervalNs()).String(),
		NodeCount:               nodeCount,
		Ephemeral:               sub.Ephemeral(),
		EphemeralNodeEvictDelay: time.Duration(sub.EphemeralNodeEvictDelayNs()).String(),
		Enabled:                 sub.Enabled(),
		CreatedAt:               time.Unix(0, sub.CreatedAtNs).UTC().Format(time.RFC3339Nano),
	}
	if lc := sub.LastCheckedNs.Load(); lc > 0 {
		resp.LastChecked = time.Unix(0, lc).UTC().Format(time.RFC3339Nano)
	}
	if lu := sub.LastUpdatedNs.Load(); lu > 0 {
		resp.LastUpdated = time.Unix(0, lu).UTC().Format(time.RFC3339Nano)
	}
	resp.LastError = sub.GetLastError()
	return resp
}

// ListSubscriptions returns all subscriptions, optionally filtered by enabled.
func (s *ControlPlaneService) ListSubscriptions(enabled *bool) ([]SubscriptionResponse, error) {
	var result []SubscriptionResponse
	s.SubMgr.Range(func(id string, sub *subscription.Subscription) bool {
		if enabled != nil && sub.Enabled() != *enabled {
			return true
		}
		result = append(result, subToResponse(sub))
		return true
	})
	if result == nil {
		result = []SubscriptionResponse{}
	}
	return result, nil
}

// GetSubscription returns a single subscription by ID.
func (s *ControlPlaneService) GetSubscription(id string) (*SubscriptionResponse, error) {
	sub := s.SubMgr.Lookup(id)
	if sub == nil {
		return nil, notFound("subscription not found")
	}
	r := subToResponse(sub)
	return &r, nil
}

// CreateSubscriptionRequest holds create subscription parameters.
type CreateSubscriptionRequest struct {
	Name                    *string `json:"name"`
	URL                     *string `json:"url"`
	UpdateInterval          *string `json:"update_interval"`
	Enabled                 *bool   `json:"enabled"`
	Ephemeral               *bool   `json:"ephemeral"`
	EphemeralNodeEvictDelay *string `json:"ephemeral_node_evict_delay"`
}

const minSubscriptionUpdateInterval = 30 * time.Second
const defaultSubscriptionEphemeralNodeEvictDelay = 72 * time.Hour

// CreateSubscription creates a new subscription.
func (s *ControlPlaneService) CreateSubscription(req CreateSubscriptionRequest) (*SubscriptionResponse, error) {
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		return nil, invalidArg("name is required")
	}
	name := strings.TrimSpace(*req.Name)

	if req.URL == nil || *req.URL == "" {
		return nil, invalidArg("url is required")
	}
	subURL := *req.URL
	if _, verr := parseHTTPAbsoluteURL("url", subURL); verr != nil {
		return nil, verr
	}

	updateInterval := 5 * time.Minute
	if req.UpdateInterval != nil {
		d, err := time.ParseDuration(*req.UpdateInterval)
		if err != nil {
			return nil, invalidArg("update_interval: " + err.Error())
		}
		if d < minSubscriptionUpdateInterval {
			return nil, invalidArg("update_interval: must be >= 30s")
		}
		updateInterval = d
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ephemeral := false
	if req.Ephemeral != nil {
		ephemeral = *req.Ephemeral
	}
	ephemeralNodeEvictDelay := defaultSubscriptionEphemeralNodeEvictDelay
	if req.EphemeralNodeEvictDelay != nil {
		d, err := time.ParseDuration(*req.EphemeralNodeEvictDelay)
		if err != nil {
			return nil, invalidArg("ephemeral_node_evict_delay: " + err.Error())
		}
		if d < 0 {
			return nil, invalidArg("ephemeral_node_evict_delay: must be non-negative")
		}
		ephemeralNodeEvictDelay = d
	}

	id := uuid.New().String()
	now := time.Now().UnixNano()

	ms := model.Subscription{
		ID:                        id,
		Name:                      name,
		URL:                       subURL,
		UpdateIntervalNs:          int64(updateInterval),
		Enabled:                   enabled,
		Ephemeral:                 ephemeral,
		EphemeralNodeEvictDelayNs: int64(ephemeralNodeEvictDelay),
		CreatedAtNs:               now,
		UpdatedAtNs:               now,
	}
	if err := s.Engine.UpsertSubscription(ms); err != nil {
		return nil, internal("persist subscription", err)
	}

	sub := subscription.NewSubscription(id, name, subURL, enabled, ephemeral)
	sub.SetFetchConfig(subURL, int64(updateInterval))
	sub.SetEphemeralNodeEvictDelayNs(int64(ephemeralNodeEvictDelay))
	sub.CreatedAtNs = now
	sub.UpdatedAtNs = now
	s.SubMgr.Register(sub)

	r := subToResponse(sub)
	return &r, nil
}

// UpdateSubscription applies a constrained partial patch to a subscription.
// This is not RFC 7396 JSON Merge Patch: patch must be a non-empty object and
// null values are rejected.
func (s *ControlPlaneService) UpdateSubscription(id string, patchJSON json.RawMessage) (*SubscriptionResponse, error) {
	patch, verr := parseMergePatch(patchJSON)
	if verr != nil {
		return nil, verr
	}
	if err := patch.validateFields(subscriptionPatchAllowedFields, func(key string) string {
		return fmt.Sprintf("field %q is read-only or unknown", key)
	}); err != nil {
		return nil, err
	}

	sub := s.SubMgr.Lookup(id)
	if sub == nil {
		return nil, notFound("subscription not found")
	}

	// Track what changed for side-effects.
	nameChanged := false
	enabledChanged := false
	urlChanged := false

	newName := sub.Name()
	if nameStr, ok, err := patch.optionalNonEmptyString("name"); err != nil {
		return nil, err
	} else if ok {
		newName = nameStr
		if newName != sub.Name() {
			nameChanged = true
		}
	}

	newURL := sub.URL()
	if urlStr, ok, err := patch.optionalString("url"); err != nil {
		return nil, err
	} else if ok {
		if _, verr := parseHTTPAbsoluteURL("url", urlStr); verr != nil {
			return nil, verr
		}
		newURL = urlStr
		if newURL != sub.URL() {
			urlChanged = true
		}
	}

	newInterval := sub.UpdateIntervalNs()
	if d, ok, err := patch.optionalDurationString("update_interval"); err != nil {
		return nil, err
	} else if ok {
		if d < minSubscriptionUpdateInterval {
			return nil, invalidArg("update_interval: must be >= 30s")
		}
		newInterval = int64(d)
	}

	newEnabled := sub.Enabled()
	if b, ok, err := patch.optionalBool("enabled"); err != nil {
		return nil, err
	} else if ok {
		if b != newEnabled {
			enabledChanged = true
		}
		newEnabled = b
	}

	newEphemeral := sub.Ephemeral()
	if b, ok, err := patch.optionalBool("ephemeral"); err != nil {
		return nil, err
	} else if ok {
		newEphemeral = b
	}

	newEphemeralNodeEvictDelay := sub.EphemeralNodeEvictDelayNs()
	if d, ok, err := patch.optionalDurationString("ephemeral_node_evict_delay"); err != nil {
		return nil, err
	} else if ok {
		if d < 0 {
			return nil, invalidArg("ephemeral_node_evict_delay: must be non-negative")
		}
		newEphemeralNodeEvictDelay = int64(d)
	}

	now := time.Now().UnixNano()
	ms := model.Subscription{
		ID:                        id,
		Name:                      newName,
		URL:                       newURL,
		UpdateIntervalNs:          newInterval,
		Enabled:                   newEnabled,
		Ephemeral:                 newEphemeral,
		EphemeralNodeEvictDelayNs: newEphemeralNodeEvictDelay,
		CreatedAtNs:               sub.CreatedAtNs,
		UpdatedAtNs:               now,
	}
	if err := s.Engine.UpsertSubscription(ms); err != nil {
		return nil, internal("persist subscription", err)
	}

	// Apply side-effects via scheduler.
	sub.SetFetchConfig(newURL, newInterval)
	sub.SetEphemeral(newEphemeral)
	sub.SetEphemeralNodeEvictDelayNs(newEphemeralNodeEvictDelay)
	sub.UpdatedAtNs = now

	if nameChanged {
		s.Scheduler.RenameSubscription(sub, newName)
	}
	if enabledChanged {
		s.Scheduler.SetSubscriptionEnabled(sub, newEnabled)
	}
	if urlChanged {
		go s.Scheduler.UpdateSubscription(sub)
	}

	r := subToResponse(sub)
	return &r, nil
}

// DeleteSubscription deletes a subscription and evicts its nodes.
func (s *ControlPlaneService) DeleteSubscription(id string) error {
	sub := s.SubMgr.Lookup(id)
	if sub == nil {
		return notFound("subscription not found")
	}

	var (
		managedHashes []node.Hash
		deleteErr     error
	)

	// Keep delete atomic across persistence + in-memory runtime state:
	// if DB delete fails, do not mutate runtime subscription/node state.
	sub.WithOpLock(func() {
		// Re-check under lock in case another goroutine removed it between
		// the initial Lookup and lock acquisition.
		lockedSub := s.SubMgr.Lookup(id)
		if lockedSub == nil {
			deleteErr = notFound("subscription not found")
			return
		}

		lockedSub.ManagedNodes().Range(func(h node.Hash, _ []string) bool {
			managedHashes = append(managedHashes, h)
			return true
		})

		if err := s.Engine.DeleteSubscription(id); err != nil {
			if errors.Is(err, state.ErrNotFound) {
				deleteErr = notFound("subscription not found")
			} else {
				deleteErr = internal("delete subscription", err)
			}
			return
		}

		// Persist succeeded; now apply in-memory cleanup.
		for _, h := range managedHashes {
			s.Pool.RemoveNodeFromSub(h, id)
		}
		s.SubMgr.Unregister(id)
	})

	return deleteErr
}

// RefreshSubscription triggers an immediate subscription refresh (blocks).
func (s *ControlPlaneService) RefreshSubscription(id string) error {
	sub := s.SubMgr.Lookup(id)
	if sub == nil {
		return notFound("subscription not found")
	}
	s.Scheduler.UpdateSubscription(sub)
	return nil
}

// CleanupSubscriptionCircuitOpenNodes removes problematic nodes from a subscription.
// It removes nodes that are currently circuit-open, and nodes with no outbound while
// carrying a non-empty last error.
func (s *ControlPlaneService) CleanupSubscriptionCircuitOpenNodes(id string) (int, error) {
	return s.cleanupSubscriptionCircuitOpenNodesWithHook(id, nil)
}

// cleanupSubscriptionCircuitOpenNodesWithHook performs cleanup with an optional
// hook between first scan and second confirmation scan. The hook is only used
// by tests to simulate TOCTOU recovery.
func (s *ControlPlaneService) cleanupSubscriptionCircuitOpenNodesWithHook(
	id string,
	betweenScans func(),
) (int, error) {
	sub := s.SubMgr.Lookup(id)
	if sub == nil {
		return 0, notFound("subscription not found")
	}

	var (
		cleanedCount int
		cleanupErr   error
	)

	sub.WithOpLock(func() {
		// Re-check under lock in case another goroutine deleted the subscription
		// between lookup and lock acquisition.
		lockedSub := s.SubMgr.Lookup(id)
		if lockedSub == nil {
			cleanupErr = notFound("subscription not found")
			return
		}

		cleanedCount = topology.CleanupSubscriptionNodesWithConfirmNoLock(
			lockedSub,
			s.Pool,
			shouldCleanupSubscriptionNode,
			betweenScans,
		)
	})

	return cleanedCount, cleanupErr
}

func shouldCleanupSubscriptionNode(entry *node.NodeEntry) bool {
	if entry == nil {
		return false
	}
	return entry.IsCircuitOpen() || (!entry.HasOutbound() && entry.GetLastError() != "")
}
