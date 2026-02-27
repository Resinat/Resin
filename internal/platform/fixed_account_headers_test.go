package platform

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeFixedAccountHeaders_Empty(t *testing.T) {
	normalized, headers, err := NormalizeFixedAccountHeaders(" \n\t ")
	if err != nil {
		t.Fatalf("NormalizeFixedAccountHeaders: %v", err)
	}
	if normalized != "" {
		t.Fatalf("normalized: got %q, want empty", normalized)
	}
	if len(headers) != 0 {
		t.Fatalf("headers: got %v, want empty", headers)
	}
}

func TestNormalizeFixedAccountHeaders_MultiLineCanonicalAndDedup(t *testing.T) {
	raw := " authorization \nX-Account-Id\r\nx-account-id\n\nX-Trace-Account "
	normalized, headers, err := NormalizeFixedAccountHeaders(raw)
	if err != nil {
		t.Fatalf("NormalizeFixedAccountHeaders: %v", err)
	}
	wantHeaders := []string{"Authorization", "X-Account-Id", "X-Trace-Account"}
	if !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("headers: got %v, want %v", headers, wantHeaders)
	}
	if normalized != strings.Join(wantHeaders, "\n") {
		t.Fatalf("normalized: got %q, want %q", normalized, strings.Join(wantHeaders, "\n"))
	}
}

func TestNormalizeFixedAccountHeaders_Invalid(t *testing.T) {
	_, _, err := NormalizeFixedAccountHeaders("Authorization\nbad header")
	if err == nil {
		t.Fatal("expected invalid header error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("unexpected error: %v", err)
	}
}
