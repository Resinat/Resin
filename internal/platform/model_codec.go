package platform

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Resinat/Resin/internal/model"
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
	emptyAccountBehavior string,
	fixedAccountHeader string,
	allocationPolicy string,
) *Platform {
	normalizedFixedHeaders, fixedHeaders, err := NormalizeFixedAccountHeaders(fixedAccountHeader)
	if err != nil {
		normalizedFixedHeaders = strings.TrimSpace(fixedAccountHeader)
		fixedHeaders = nil
	}
	plat := NewPlatform(id, name, regexFilters, regionFilters)
	plat.StickyTTLNs = stickyTTLNs
	plat.ReverseProxyMissAction = missAction
	plat.ReverseProxyEmptyAccountBehavior = emptyAccountBehavior
	plat.ReverseProxyFixedAccountHeader = normalizedFixedHeaders
	plat.ReverseProxyFixedAccountHeaders = append([]string(nil), fixedHeaders...)
	plat.AllocationPolicy = ParseAllocationPolicy(allocationPolicy)
	return plat
}

// CompileModelRegexFilters compiles regex filters from persisted model values.
func CompileModelRegexFilters(platformID string, regexFilters []string) ([]*regexp.Regexp, error) {
	compiled, err := CompileRegexFilters(regexFilters)
	if err != nil {
		return nil, fmt.Errorf("decode platform %s regex_filters: %w", platformID, err)
	}
	return compiled, nil
}

// BuildFromModel builds a runtime platform from a persisted model.Platform.
func BuildFromModel(mp model.Platform) (*Platform, error) {
	regexFilters, err := CompileModelRegexFilters(mp.ID, mp.RegexFilters)
	if err != nil {
		return nil, err
	}
	if err := ValidateRegionFilters(mp.RegionFilters); err != nil {
		return nil, err
	}
	emptyAccountBehavior := mp.ReverseProxyEmptyAccountBehavior
	if !ReverseProxyEmptyAccountBehavior(emptyAccountBehavior).IsValid() {
		emptyAccountBehavior = string(ReverseProxyEmptyAccountBehaviorRandom)
	}
	fixedHeader, _, err := NormalizeFixedAccountHeaders(mp.ReverseProxyFixedAccountHeader)
	if err != nil {
		return nil, fmt.Errorf("decode platform %s reverse_proxy_fixed_account_header: %w", mp.ID, err)
	}
	if emptyAccountBehavior == string(ReverseProxyEmptyAccountBehaviorFixedHeader) && fixedHeader == "" {
		return nil, fmt.Errorf(
			"decode platform %s reverse_proxy_fixed_account_header: required when reverse_proxy_empty_account_behavior is %s",
			mp.ID,
			ReverseProxyEmptyAccountBehaviorFixedHeader,
		)
	}

	return NewConfiguredPlatform(
		mp.ID,
		mp.Name,
		regexFilters,
		append([]string(nil), mp.RegionFilters...),
		mp.StickyTTLNs,
		mp.ReverseProxyMissAction,
		emptyAccountBehavior,
		fixedHeader,
		mp.AllocationPolicy,
	), nil
}
