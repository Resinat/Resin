// Package node provides core node types and operations for the global node pool.
package node

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/zeebo/xxh3"
)

// Hash is a 128-bit node identity derived from canonical JSON of the node's
// raw options (with the "tag" field removed). Two nodes with identical
// configuration (ignoring tag) produce the same Hash.
type Hash [16]byte

// Zero is the zero-value Hash.
var Zero Hash

// HashFromRawOptions computes a node Hash from raw JSON options.
// It unmarshals the JSON, removes the "tag" key, and re-marshals.
// Go's encoding/json sorts map keys at all nesting levels, so the output
// is deterministic without any manual sorting.
// If JSON parsing fails, it falls back to hashing the raw bytes directly.
func HashFromRawOptions(raw []byte) Hash {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return hashBytes(raw)
	}
	delete(m, "tag")

	canonical, err := json.Marshal(m)
	if err != nil {
		return hashBytes(raw)
	}
	return hashBytes(canonical)
}

// Hex returns the lowercase hex encoding of the hash.
func (h Hash) Hex() string {
	return hex.EncodeToString(h[:])
}

// String implements fmt.Stringer.
func (h Hash) String() string {
	return h.Hex()
}

// IsZero reports whether h is the zero hash.
func (h Hash) IsZero() bool {
	return h == Zero
}

// ParseHex decodes a 32-character hex string into a Hash.
func ParseHex(s string) (Hash, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return Zero, fmt.Errorf("node.ParseHex: %w", err)
	}
	if len(b) != 16 {
		return Zero, fmt.Errorf("node.ParseHex: expected 16 bytes, got %d", len(b))
	}
	var h Hash
	copy(h[:], b)
	return h, nil
}

// hashBytes computes xxh3-128 of the given bytes and returns it as a Hash.
func hashBytes(data []byte) Hash {
	h128 := xxh3.Hash128(data)
	var h Hash
	binary.LittleEndian.PutUint64(h[:8], h128.Lo)
	binary.LittleEndian.PutUint64(h[8:], h128.Hi)
	return h
}
