package platform

import "testing"

func TestReverseProxyMissActionIsValid(t *testing.T) {
	valid := []ReverseProxyMissAction{
		ReverseProxyMissActionRandom,
		ReverseProxyMissActionReject,
	}
	for _, action := range valid {
		if !action.IsValid() {
			t.Fatalf("expected valid miss action %q", action)
		}
	}

	if ReverseProxyMissAction("INVALID").IsValid() {
		t.Fatal("expected invalid miss action to fail validation")
	}
}
