package platform

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Resinat/Resin/internal/model"
)

func TestBuildFromModel_Success(t *testing.T) {
	mp := model.Platform{
		ID:                               "plat-1",
		Name:                             "Platform-1",
		StickyTTLNs:                      3600,
		RegexFilters:                     []string{`^us-.*$`},
		RegionFilters:                    []string{"us", "jp"},
		ReverseProxyMissAction:           "REJECT",
		ReverseProxyEmptyAccountBehavior: "FIXED_HEADER",
		ReverseProxyFixedAccountHeader:   "x-account-id",
		AllocationPolicy:                 "PREFER_LOW_LATENCY",
	}

	plat, err := BuildFromModel(mp)
	if err != nil {
		t.Fatalf("BuildFromModel: %v", err)
	}

	if plat.ID != mp.ID || plat.Name != mp.Name {
		t.Fatalf("id/name mismatch: got (%q,%q)", plat.ID, plat.Name)
	}
	if plat.StickyTTLNs != mp.StickyTTLNs {
		t.Fatalf("sticky ttl mismatch: got %d want %d", plat.StickyTTLNs, mp.StickyTTLNs)
	}
	if plat.ReverseProxyMissAction != mp.ReverseProxyMissAction {
		t.Fatalf("miss action mismatch: got %q want %q", plat.ReverseProxyMissAction, mp.ReverseProxyMissAction)
	}
	if plat.ReverseProxyEmptyAccountBehavior != "FIXED_HEADER" {
		t.Fatalf(
			"empty-account behavior mismatch: got %q want %q",
			plat.ReverseProxyEmptyAccountBehavior,
			"FIXED_HEADER",
		)
	}
	if plat.ReverseProxyFixedAccountHeader != "X-Account-Id" {
		t.Fatalf(
			"fixed account header mismatch: got %q want %q",
			plat.ReverseProxyFixedAccountHeader,
			"X-Account-Id",
		)
	}
	if plat.AllocationPolicy != AllocationPolicyPreferLowLatency {
		t.Fatalf("allocation policy mismatch: got %q want %q", plat.AllocationPolicy, AllocationPolicyPreferLowLatency)
	}
	if len(plat.RegexFilters) != 1 || !plat.RegexFilters[0].MatchString("us-node") {
		t.Fatalf("regex filters not compiled as expected: %+v", plat.RegexFilters)
	}
	if len(plat.RegionFilters) != 2 || plat.RegionFilters[0] != "us" || plat.RegionFilters[1] != "jp" {
		t.Fatalf("region filters mismatch: %+v", plat.RegionFilters)
	}
}

func TestBuildFromModel_InvalidRegex(t *testing.T) {
	_, err := BuildFromModel(model.Platform{
		ID:           "plat-1",
		RegexFilters: []string{`(broken`},
	})
	if err == nil {
		t.Fatal("expected regex decode error")
	}
	if !strings.Contains(err.Error(), "regex_filters") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildFromModel_InvalidRegionFilters(t *testing.T) {
	_, err := BuildFromModel(model.Platform{
		ID:            "plat-1",
		RegexFilters:  []string{},
		RegionFilters: []string{"US"},
	})
	if err == nil {
		t.Fatal("expected region decode error")
	}
	if !strings.Contains(err.Error(), "region_filters[0]") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildFromModel_InvalidEmptyAccountBehaviorFallsBackToRandom(t *testing.T) {
	plat, err := BuildFromModel(model.Platform{
		ID:                               "plat-1",
		Name:                             "Platform-1",
		RegexFilters:                     []string{},
		RegionFilters:                    []string{},
		ReverseProxyMissAction:           "RANDOM",
		ReverseProxyEmptyAccountBehavior: "INVALID",
		AllocationPolicy:                 "BALANCED",
	})
	if err != nil {
		t.Fatalf("BuildFromModel: %v", err)
	}
	if plat.ReverseProxyEmptyAccountBehavior != string(ReverseProxyEmptyAccountBehaviorRandom) {
		t.Fatalf(
			"empty-account behavior fallback mismatch: got %q, want %q",
			plat.ReverseProxyEmptyAccountBehavior,
			ReverseProxyEmptyAccountBehaviorRandom,
		)
	}
}

func TestBuildFromModel_FixedHeadersMultiLineNormalized(t *testing.T) {
	plat, err := BuildFromModel(model.Platform{
		ID:                               "plat-1",
		Name:                             "Platform-1",
		RegexFilters:                     []string{},
		RegionFilters:                    []string{},
		ReverseProxyMissAction:           "RANDOM",
		ReverseProxyEmptyAccountBehavior: "FIXED_HEADER",
		ReverseProxyFixedAccountHeader:   " authorization \nX-Account-Id\nx-account-id",
		AllocationPolicy:                 "BALANCED",
	})
	if err != nil {
		t.Fatalf("BuildFromModel: %v", err)
	}

	if plat.ReverseProxyFixedAccountHeader != "Authorization\nX-Account-Id" {
		t.Fatalf(
			"fixed account header mismatch: got %q, want %q",
			plat.ReverseProxyFixedAccountHeader,
			"Authorization\nX-Account-Id",
		)
	}
	if !reflect.DeepEqual(plat.ReverseProxyFixedAccountHeaders, []string{"Authorization", "X-Account-Id"}) {
		t.Fatalf(
			"fixed account headers mismatch: got %v, want %v",
			plat.ReverseProxyFixedAccountHeaders,
			[]string{"Authorization", "X-Account-Id"},
		)
	}
}

func TestBuildFromModel_FixedHeaderRequiresValidHeaderName(t *testing.T) {
	_, err := BuildFromModel(model.Platform{
		ID:                               "plat-1",
		RegexFilters:                     []string{},
		RegionFilters:                    []string{},
		ReverseProxyEmptyAccountBehavior: "FIXED_HEADER",
		ReverseProxyFixedAccountHeader:   "bad header",
	})
	if err == nil {
		t.Fatal("expected fixed header validation error")
	}
	if !strings.Contains(err.Error(), "reverse_proxy_fixed_account_header") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileRegexFilters_Invalid(t *testing.T) {
	_, err := CompileRegexFilters([]string{"(broken"})
	if err == nil {
		t.Fatal("expected compile error")
	}
	if !strings.Contains(err.Error(), "regex_filters[0]") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRegionFilters_Invalid(t *testing.T) {
	err := ValidateRegionFilters([]string{"US"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "region_filters[0]") {
		t.Fatalf("unexpected error: %v", err)
	}
}
