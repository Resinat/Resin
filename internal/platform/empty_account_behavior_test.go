package platform

import "testing"

func TestReverseProxyEmptyAccountBehaviorIsValid(t *testing.T) {
	valid := []ReverseProxyEmptyAccountBehavior{
		ReverseProxyEmptyAccountBehaviorRandom,
		ReverseProxyEmptyAccountBehaviorFixedHeader,
		ReverseProxyEmptyAccountBehaviorAccountHeaderRule,
	}
	for _, behavior := range valid {
		if !behavior.IsValid() {
			t.Fatalf("expected valid empty account behavior %q", behavior)
		}
	}

	if ReverseProxyEmptyAccountBehavior("INVALID").IsValid() {
		t.Fatal("expected invalid empty account behavior to fail validation")
	}
}
