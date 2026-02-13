package node

import (
	"testing"
)

func TestHashFromRawOptions_Deterministic(t *testing.T) {
	raw := []byte(`{"type":"shadowsocks","server":"1.2.3.4","server_port":443,"method":"aes-256-gcm","password":"secret"}`)
	h1 := HashFromRawOptions(raw)
	h2 := HashFromRawOptions(raw)
	if h1 != h2 {
		t.Fatalf("same input produced different hashes: %s vs %s", h1.Hex(), h2.Hex())
	}
	if h1.IsZero() {
		t.Fatal("hash should not be zero for valid input")
	}
}

func TestHashFromRawOptions_IgnoresTag(t *testing.T) {
	withTag := []byte(`{"type":"shadowsocks","tag":"us-node-1","server":"1.2.3.4","server_port":443}`)
	withoutTag := []byte(`{"type":"shadowsocks","server":"1.2.3.4","server_port":443}`)
	differentTag := []byte(`{"type":"shadowsocks","tag":"jp-node-2","server":"1.2.3.4","server_port":443}`)

	h1 := HashFromRawOptions(withTag)
	h2 := HashFromRawOptions(withoutTag)
	h3 := HashFromRawOptions(differentTag)

	if h1 != h2 {
		t.Fatalf("tag should be ignored: with-tag=%s, without-tag=%s", h1.Hex(), h2.Hex())
	}
	if h1 != h3 {
		t.Fatalf("different tags should produce same hash: %s vs %s", h1.Hex(), h3.Hex())
	}
}

func TestHashFromRawOptions_DifferentConfigs(t *testing.T) {
	a := []byte(`{"type":"shadowsocks","server":"1.2.3.4","server_port":443}`)
	b := []byte(`{"type":"shadowsocks","server":"5.6.7.8","server_port":443}`)

	ha := HashFromRawOptions(a)
	hb := HashFromRawOptions(b)
	if ha == hb {
		t.Fatal("different configs should produce different hashes")
	}
}

func TestHashFromRawOptions_KeyOrderIndependent(t *testing.T) {
	a := []byte(`{"type":"shadowsocks","server":"1.2.3.4","server_port":443}`)
	b := []byte(`{"server_port":443,"server":"1.2.3.4","type":"shadowsocks"}`)

	ha := HashFromRawOptions(a)
	hb := HashFromRawOptions(b)
	if ha != hb {
		t.Fatalf("key order should not affect hash: %s vs %s", ha.Hex(), hb.Hex())
	}
}

func TestHashFromRawOptions_InvalidJSON_Fallback(t *testing.T) {
	raw := []byte(`not valid json`)
	h := HashFromRawOptions(raw)
	if h.IsZero() {
		t.Fatal("invalid JSON should still produce a non-zero hash via fallback")
	}

	// Fallback should be deterministic.
	h2 := HashFromRawOptions(raw)
	if h != h2 {
		t.Fatalf("fallback hash not deterministic: %s vs %s", h.Hex(), h2.Hex())
	}
}

func TestHexRoundTrip(t *testing.T) {
	raw := []byte(`{"type":"vmess","server":"example.com"}`)
	original := HashFromRawOptions(raw)

	hexStr := original.Hex()
	if len(hexStr) != 32 {
		t.Fatalf("hex string should be 32 chars, got %d: %s", len(hexStr), hexStr)
	}

	parsed, err := ParseHex(hexStr)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != original {
		t.Fatalf("round-trip failed: %s != %s", parsed.Hex(), original.Hex())
	}
}

func TestParseHex_Errors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"too short", "abcd"},
		{"too long", "aabbccddaabbccddaabbccddaabbccddaa"},
		{"invalid chars", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseHex(tt.input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestHash_IsZero(t *testing.T) {
	var h Hash
	if !h.IsZero() {
		t.Fatal("default Hash should be zero")
	}

	h2 := HashFromRawOptions([]byte(`{"type":"ss"}`))
	if h2.IsZero() {
		t.Fatal("computed Hash should not be zero")
	}
}
