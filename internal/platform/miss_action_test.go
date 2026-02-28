package platform

import "testing"

func TestReverseProxyMissActionIsValid(t *testing.T) {
	valid := []ReverseProxyMissAction{
		ReverseProxyMissActionTreatAsEmpty,
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

func TestNormalizeReverseProxyMissAction(t *testing.T) {
	if got := NormalizeReverseProxyMissAction(" TREAT_AS_EMPTY "); got != ReverseProxyMissActionTreatAsEmpty {
		t.Fatalf("normalize TREAT_AS_EMPTY: got %q want %q", got, ReverseProxyMissActionTreatAsEmpty)
	}
	if got := NormalizeReverseProxyMissAction("REJECT"); got != ReverseProxyMissActionReject {
		t.Fatalf("normalize REJECT: got %q want %q", got, ReverseProxyMissActionReject)
	}
	if got := NormalizeReverseProxyMissAction("RANDOM"); got != "" {
		t.Fatalf("normalize RANDOM: got %q want empty", got)
	}
	if got := NormalizeReverseProxyMissAction("unknown"); got != "" {
		t.Fatalf("normalize unknown: got %q want empty", got)
	}
}
