package platform

import (
	"strings"
	"testing"

	"github.com/Resinat/Resin/internal/model"
)

func TestBuildFromModel_Success(t *testing.T) {
	mp := model.Platform{
		ID:                     "plat-1",
		Name:                   "Platform-1",
		StickyTTLNs:            3600,
		RegexFilters:           []string{`^us-.*$`},
		RegionFilters:          []string{"us", "jp"},
		ReverseProxyMissAction: "REJECT",
		AllocationPolicy:       "PREFER_LOW_LATENCY",
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
