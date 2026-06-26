package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/subscription"
)

// ------------------------------------------------------------------
// Export / Import data types
// ------------------------------------------------------------------

const exportVersion = 1

// ExportEntry is a portable config object. Fields are filtered from the API
// response by the create/patch allowlists so export stays in sync with config.
type ExportEntry map[string]json.RawMessage

// ExportPayload is the top-level JSON structure for data export/import.
type ExportPayload struct {
	Version       int           `json:"version"`
	ExportedAt    string        `json:"exported_at"`
	Platforms     []ExportEntry `json:"platforms"`
	Subscriptions []ExportEntry `json:"subscriptions"`
}

// ImportResult summarises what happened during an import.
type ImportResult struct {
	PlatformsCreated         int      `json:"platforms_created"`
	PlatformsSkipped         int      `json:"platforms_skipped"`
	PlatformsOverwritten     int      `json:"platforms_overwritten"`
	SubscriptionsCreated     int      `json:"subscriptions_created"`
	SubscriptionsSkipped     int      `json:"subscriptions_skipped"`
	SubscriptionsOverwritten int      `json:"subscriptions_overwritten"`
	Errors                   []string `json:"errors"`
}

// ------------------------------------------------------------------
// Export
// ------------------------------------------------------------------

// ExportData builds an ExportPayload containing all user-created platforms
// and all subscriptions.
func (s *ControlPlaneService) ExportData() (*ExportPayload, error) {
	platforms, err := s.Engine.ListPlatforms()
	if err != nil {
		return nil, internal("list platforms for export", err)
	}

	exportPlatforms := make([]ExportEntry, 0, len(platforms))
	for _, p := range platforms {
		if p.ID == platform.DefaultPlatformID {
			continue
		}
		entry, err := exportEntryFrom(platformToResponse(p), isPlatformConfigExportField)
		if err != nil {
			return nil, internal("encode platform for export", err)
		}
		exportPlatforms = append(exportPlatforms, entry)
	}

	subs, err := s.ListSubscriptions(nil)
	if err != nil {
		return nil, internal("list subscriptions for export", err)
	}

	exportSubs := make([]ExportEntry, 0, len(subs))
	for _, sub := range subs {
		entry, err := exportEntryFrom(sub, isSubscriptionConfigExportField)
		if err != nil {
			return nil, internal("encode subscription for export", err)
		}
		exportSubs = append(exportSubs, entry)
	}

	return &ExportPayload{
		Version:       exportVersion,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Platforms:     exportPlatforms,
		Subscriptions: exportSubs,
	}, nil
}

// ------------------------------------------------------------------
// Import
// ------------------------------------------------------------------

// ImportData imports platforms and subscriptions from the given payload.
// strategy must be "skip" (default) or "overwrite".
func (s *ControlPlaneService) ImportData(payload ExportPayload, strategy string) (*ImportResult, error) {
	if strategy == "" {
		strategy = "skip"
	}
	if strategy != "skip" && strategy != "overwrite" {
		return nil, invalidArg("strategy must be 'skip' or 'overwrite'")
	}

	result := &ImportResult{Errors: []string{}}

	s.importPlatforms(payload.Platforms, strategy, result)
	s.importSubscriptions(payload.Subscriptions, strategy, result)

	return result, nil
}

func (s *ControlPlaneService) importPlatforms(entries []ExportEntry, strategy string, result *ImportResult) {
	existing, err := s.Engine.ListPlatforms()
	if err != nil {
		result.Errors = append(result.Errors, "failed to list existing platforms: "+err.Error())
		return
	}
	nameToID := make(map[string]string, len(existing))
	for _, p := range existing {
		nameToID[p.Name] = p.ID
	}

	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		name, err := requiredEntryString(entry, "name")
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("platforms[%d]: %v, skipped", i, err))
			continue
		}
		if name == "" {
			result.Errors = append(result.Errors, fmt.Sprintf("platforms[%d]: name is empty, skipped", i))
			continue
		}
		if seen[name] {
			result.Errors = append(result.Errors, fmt.Sprintf("platforms[%d]: duplicate name %q in import payload, skipped", i, name))
			continue
		}
		seen[name] = true

		configEntry := filterEntry(entry, isPlatformConfigExportField)
		existingID, exists := nameToID[name]
		if exists && strategy == "skip" {
			result.PlatformsSkipped++
			continue
		}

		if exists && strategy == "overwrite" {
			patchJSON, err := marshalEntry(configEntry)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("platform %q: encode patch failed: %v", name, err))
				continue
			}
			if _, err := s.UpdatePlatform(existingID, patchJSON); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("platform %q: overwrite failed: %v", name, err))
				continue
			}
			result.PlatformsOverwritten++
			continue
		}

		req, err := decodeCreatePlatformRequest(configEntry)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("platform %q: decode failed: %v", name, err))
			continue
		}
		if _, err := s.CreatePlatform(req); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("platform %q: create failed: %v", name, err))
			continue
		}
		result.PlatformsCreated++
	}
}

func (s *ControlPlaneService) importSubscriptions(entries []ExportEntry, strategy string, result *ImportResult) {
	existingSubs, err := s.ListSubscriptions(nil)
	if err != nil {
		result.Errors = append(result.Errors, "failed to list existing subscriptions: "+err.Error())
		return
	}
	nameToID := make(map[string]string, len(existingSubs))
	urlToID := make(map[string]string, len(existingSubs))
	idToSourceType := make(map[string]string, len(existingSubs))
	for _, sub := range existingSubs {
		nameToID[sub.Name] = sub.ID
		idToSourceType[sub.ID] = sub.SourceType
		if sub.URL != "" {
			urlToID[sub.URL] = sub.ID
		}
	}

	seenName := make(map[string]bool, len(entries))
	seenURL := make(map[string]bool, len(entries))
	for i, entry := range entries {
		name, err := requiredEntryString(entry, "name")
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("subscriptions[%d]: %v, skipped", i, err))
			continue
		}
		if name == "" {
			result.Errors = append(result.Errors, fmt.Sprintf("subscriptions[%d]: name is empty, skipped", i))
			continue
		}
		if seenName[name] {
			result.Errors = append(result.Errors, fmt.Sprintf("subscriptions[%d]: duplicate name %q in import payload, skipped", i, name))
			continue
		}
		seenName[name] = true

		if _, exists := nameToID[name]; exists && strategy == "skip" {
			result.SubscriptionsSkipped++
			continue
		}

		sourceType, err := entrySourceType(entry)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("subscriptions[%d]: %v, skipped", i, err))
			continue
		}

		url := ""
		if sourceType == subscription.SourceTypeRemote {
			url, err = optionalEntryString(entry, "url")
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("subscriptions[%d]: %v, skipped", i, err))
				continue
			}
			url = strings.TrimSpace(url)
			if url != "" {
				if seenURL[url] {
					result.Errors = append(result.Errors, fmt.Sprintf("subscriptions[%d]: duplicate url %q in import payload, skipped", i, url))
					continue
				}
				seenURL[url] = true
			}
		}

		existingID := ""
		if id, ok := nameToID[name]; ok {
			existingID = id
		} else if sourceType == subscription.SourceTypeRemote && url != "" {
			if id, ok := urlToID[url]; ok {
				existingID = id
			}
		}

		if existingID != "" && strategy == "skip" {
			result.SubscriptionsSkipped++
			continue
		}

		configEntry := filterEntry(entry, isSubscriptionConfigExportField)
		if existingID != "" && strategy == "overwrite" {
			if existingSourceType := idToSourceType[existingID]; existingSourceType != "" && existingSourceType != sourceType {
				result.Errors = append(result.Errors, fmt.Sprintf("subscription %q: source_type mismatch (%s != %s), skipped", name, sourceType, existingSourceType))
				continue
			}
			patchEntry := filterEntry(configEntry, func(key string) bool {
				return key != "source_type"
			})
			patchJSON, err := marshalEntry(patchEntry)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("subscription %q: encode patch failed: %v", name, err))
				continue
			}
			if _, err := s.UpdateSubscription(existingID, patchJSON); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("subscription %q: overwrite failed: %v", name, err))
				continue
			}
			result.SubscriptionsOverwritten++
			continue
		}

		req, err := decodeCreateSubscriptionRequest(configEntry)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("subscription %q: decode failed: %v", name, err))
			continue
		}
		if _, err := s.CreateSubscription(req); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("subscription %q: create failed: %v", name, err))
			continue
		}
		result.SubscriptionsCreated++
	}
}

func exportEntryFrom(value any, allow func(string) bool) (ExportEntry, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	return filterEntry(object, allow), nil
}

func filterEntry(entry ExportEntry, allow func(string) bool) ExportEntry {
	out := make(ExportEntry, len(entry))
	for key, raw := range entry {
		if allow(key) {
			out[key] = raw
		}
	}
	return out
}

func marshalEntry(entry ExportEntry) (json.RawMessage, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func isPlatformConfigExportField(key string) bool {
	return platformPatchAllowedFields[key]
}

func isSubscriptionConfigExportField(key string) bool {
	return subscriptionPatchAllowedFields[key] || key == "source_type"
}

func requiredEntryString(entry ExportEntry, field string) (string, error) {
	value, err := optionalEntryString(entry, field)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func optionalEntryString(entry ExportEntry, field string) (string, error) {
	raw, ok := entry[field]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s: must be a string", field)
	}
	return value, nil
}

func entrySourceType(entry ExportEntry) (string, error) {
	sourceType, err := optionalEntryString(entry, "source_type")
	if err != nil {
		return "", err
	}
	sourceType = strings.ToLower(strings.TrimSpace(sourceType))
	if sourceType == "" {
		return subscription.SourceTypeRemote, nil
	}
	if sourceType != subscription.SourceTypeRemote && sourceType != subscription.SourceTypeLocal {
		return "", fmt.Errorf("source_type: must be remote or local")
	}
	return sourceType, nil
}

func decodeCreatePlatformRequest(entry ExportEntry) (CreatePlatformRequest, error) {
	data, err := marshalEntry(entry)
	if err != nil {
		return CreatePlatformRequest{}, err
	}
	var req CreatePlatformRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return CreatePlatformRequest{}, err
	}
	return req, nil
}

func decodeCreateSubscriptionRequest(entry ExportEntry) (CreateSubscriptionRequest, error) {
	data, err := marshalEntry(entry)
	if err != nil {
		return CreateSubscriptionRequest{}, err
	}
	var req CreateSubscriptionRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return CreateSubscriptionRequest{}, err
	}
	return req, nil
}
