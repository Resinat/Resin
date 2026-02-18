package platform

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/resin-proxy/resin/internal/model"
)

func isLowerAlpha2(s string) bool {
	if len(s) != 2 {
		return false
	}
	return s[0] >= 'a' && s[0] <= 'z' && s[1] >= 'a' && s[1] <= 'z'
}

// ValidateRegionFilters validates region filters against lowercase ISO alpha-2 format.
func ValidateRegionFilters(regionFilters []string) error {
	for i, r := range regionFilters {
		if !isLowerAlpha2(r) {
			return fmt.Errorf("region_filters[%d]: must be a 2-letter lowercase ISO 3166-1 alpha-2 code (e.g. us, jp)", i)
		}
	}
	return nil
}

// CompileRegexFilters compiles regex filters in order.
func CompileRegexFilters(regexFilters []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(regexFilters))
	for i, re := range regexFilters {
		c, err := regexp.Compile(re)
		if err != nil {
			return nil, fmt.Errorf("regex_filters[%d]: invalid regex: %v", i, err)
		}
		compiled = append(compiled, c)
	}
	return compiled, nil
}

// NewConfiguredPlatform builds a runtime platform with non-filter settings applied.
func NewConfiguredPlatform(
	id, name string,
	regexFilters []*regexp.Regexp,
	regionFilters []string,
	stickyTTLNs int64,
	missAction string,
	allocationPolicy string,
) *Platform {
	plat := NewPlatform(id, name, regexFilters, regionFilters)
	plat.StickyTTLNs = stickyTTLNs
	plat.ReverseProxyMissAction = missAction
	plat.AllocationPolicy = ParseAllocationPolicy(allocationPolicy)
	return plat
}

// DecodeRegexFiltersJSON decodes and compiles platform regex filters from JSON.
func DecodeRegexFiltersJSON(platformID, raw string) ([]*regexp.Regexp, error) {
	var regexStrs []string
	if err := json.Unmarshal([]byte(raw), &regexStrs); err != nil {
		return nil, fmt.Errorf("decode platform %s regex_filters_json: %w", platformID, err)
	}
	compiled, err := CompileRegexFilters(regexStrs)
	if err != nil {
		return nil, fmt.Errorf("decode platform %s regex_filters_json: %w", platformID, err)
	}
	return compiled, nil
}

// DecodeRegionFiltersJSON decodes platform region filters from JSON.
func DecodeRegionFiltersJSON(platformID, raw string) ([]string, error) {
	var regionFilters []string
	if err := json.Unmarshal([]byte(raw), &regionFilters); err != nil {
		return nil, fmt.Errorf("decode platform %s region_filters_json: %w", platformID, err)
	}
	if regionFilters == nil {
		regionFilters = []string{}
	}
	return regionFilters, nil
}

// BuildFromModel builds a runtime platform from a persisted model.Platform.
func BuildFromModel(mp model.Platform) (*Platform, error) {
	regexFilters, err := DecodeRegexFiltersJSON(mp.ID, mp.RegexFiltersJSON)
	if err != nil {
		return nil, err
	}
	regionFilters, err := DecodeRegionFiltersJSON(mp.ID, mp.RegionFiltersJSON)
	if err != nil {
		return nil, err
	}

	return NewConfiguredPlatform(
		mp.ID,
		mp.Name,
		regexFilters,
		regionFilters,
		mp.StickyTTLNs,
		mp.ReverseProxyMissAction,
		mp.AllocationPolicy,
	), nil
}
