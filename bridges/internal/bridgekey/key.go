// Package bridgekey provides the collision-free encoding used when an
// external protocol identifier must be namespaced before it becomes an
// adaptor Thread key.
package bridgekey

import "strconv"

const prefix = "agent-adaptor:bridge-key:v1:"

// Encode encodes an ordered tuple without using an escapable delimiter.
// Length-prefixing makes Encode("a/b", "c") distinct from
// Encode("a", "b/c") for arbitrary UTF-8 strings.
func Encode(parts ...string) string {
	encoded := prefix + strconv.Itoa(len(parts)) + ":"
	for _, part := range parts {
		encoded += strconv.Itoa(len(part)) + ":" + part
	}
	return encoded
}
