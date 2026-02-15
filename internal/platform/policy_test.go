package platform

import "testing"

func TestParseAllocationPolicy(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want AllocationPolicy
	}{
		{name: "balanced", in: "BALANCED", want: AllocationPolicyBalanced},
		{name: "prefer_low_latency", in: "PREFER_LOW_LATENCY", want: AllocationPolicyPreferLowLatency},
		{name: "prefer_idle_ip", in: "PREFER_IDLE_IP", want: AllocationPolicyPreferIdleIP},
		{name: "invalid_fallback", in: "UNKNOWN", want: AllocationPolicyBalanced},
		{name: "empty_fallback", in: "", want: AllocationPolicyBalanced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseAllocationPolicy(tt.in); got != tt.want {
				t.Fatalf("ParseAllocationPolicy(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAllocationPolicyIsValid(t *testing.T) {
	valid := []AllocationPolicy{
		AllocationPolicyBalanced,
		AllocationPolicyPreferLowLatency,
		AllocationPolicyPreferIdleIP,
	}
	for _, p := range valid {
		if !p.IsValid() {
			t.Fatalf("expected valid policy %q", p)
		}
	}
	if AllocationPolicy("INVALID").IsValid() {
		t.Fatal("expected invalid policy to fail validation")
	}
}
