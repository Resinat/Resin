package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type mergePatch map[string]any

type unknownFieldMessage func(field string) string

// parseMergePatch parses the project's constrained PATCH body format.
// It intentionally differs from RFC 7396 JSON Merge Patch:
//   - only JSON object is accepted;
//   - object must be non-empty;
//   - null field values are rejected in validateFields.
func parseMergePatch(patchJSON json.RawMessage) (mergePatch, *ServiceError) {
	var patch map[string]any
	if err := json.Unmarshal(patchJSON, &patch); err != nil {
		return nil, invalidArg("invalid JSON: " + err.Error())
	}
	if len(patch) == 0 {
		return nil, invalidArg("empty patch")
	}
	return mergePatch(patch), nil
}

func (p mergePatch) validateFields(allowed map[string]bool, unknownMsg unknownFieldMessage) *ServiceError {
	for key, val := range p {
		if !allowed[key] {
			return invalidArg(unknownMsg(key))
		}
		if val == nil {
			return invalidArg(fmt.Sprintf("null value not allowed for field: %q", key))
		}
	}
	return nil
}

func (p mergePatch) optionalString(field string) (string, bool, *ServiceError) {
	raw, ok := p[field]
	if !ok {
		return "", false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", true, invalidArg(fmt.Sprintf("%s: must be a string", field))
	}
	return value, true, nil
}

func (p mergePatch) optionalNonEmptyString(field string) (string, bool, *ServiceError) {
	raw, ok := p[field]
	if !ok {
		return "", false, nil
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", true, invalidArg(fmt.Sprintf("%s: must be a non-empty string", field))
	}
	return strings.TrimSpace(value), true, nil
}

func (p mergePatch) optionalBool(field string) (bool, bool, *ServiceError) {
	raw, ok := p[field]
	if !ok {
		return false, false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, true, invalidArg(fmt.Sprintf("%s: must be a boolean", field))
	}
	return value, true, nil
}

func (p mergePatch) optionalStringSlice(field string) ([]string, bool, *ServiceError) {
	raw, ok := p[field]
	if !ok {
		return nil, false, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, true, invalidArg(fmt.Sprintf("%s: must be an array", field))
	}
	value := make([]string, len(arr))
	for i, item := range arr {
		itemStr, ok := item.(string)
		if !ok {
			return nil, true, invalidArg(fmt.Sprintf("%s[%d]: must be a string", field, i))
		}
		value[i] = itemStr
	}
	return value, true, nil
}

func (p mergePatch) optionalDurationString(field string) (time.Duration, bool, *ServiceError) {
	raw, ok := p[field]
	if !ok {
		return 0, false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return 0, true, invalidArg(fmt.Sprintf("%s: must be a string", field))
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, true, invalidArg(fmt.Sprintf("%s: %s", field, err.Error()))
	}
	return d, true, nil
}

func parseHTTPAbsoluteURL(field, value string) (*url.URL, *ServiceError) {
	u, err := url.ParseRequestURI(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, invalidArg(fmt.Sprintf("%s: must be an http/https absolute URL", field))
	}
	return u, nil
}
