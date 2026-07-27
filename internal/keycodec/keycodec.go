// Package keycodec provides a collision-free encoding for composite keys
// used by internal indexes and lease targets.
//
// Callers must keep host-owned v1 Thread keys unchanged in records and public
// APIs. Encode is only for internal compound identities such as a legacy
// namespace/key tuple or a domain-qualified lease target.
package keycodec

import (
	"encoding/base64"
	"encoding/binary"
)

const prefix = "kc1_"

// Encode returns a deterministic, URL-safe, injective encoding of parts.
// The field count and every byte length are encoded independently, so empty
// fields, embedded NULs, arbitrary Unicode, and different field boundaries
// cannot collide.
func Encode(parts ...string) string {
	// Avoid capacity arithmetic over attacker-controlled string lengths;
	// append's normal growth is bounded by the actual encoded input.
	raw := make([]byte, 0, binary.MaxVarintLen64)
	raw = binary.AppendUvarint(raw, uint64(len(parts)))
	for _, part := range parts {
		raw = binary.AppendUvarint(raw, uint64(len(part)))
		raw = append(raw, part...)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw)
}
