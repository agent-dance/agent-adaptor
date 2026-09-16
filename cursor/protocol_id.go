package cursor

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/internal/capabilityobs"
)

const cursorEncodedCallIDPrefix = "cursor:call-id:"

// Cursor's official print stream can include LF in an opaque call_id. Do
// not parse its components or require model-specific prefixes. Normalize the
// full UTF-8 value identically for Transcript and capability lifecycles.
// Reserve the encoding
// domain even for safe provider IDs so a literal ID cannot collide with one.
// Raw retains the exact original frame.
func cursorProtocolCallID(id string) (string, bool) {
	if capabilityobs.ValidText(id, 2048, true) {
		if !strings.HasPrefix(id, cursorEncodedCallIDPrefix) {
			return id, true
		}
	}
	if id == "" || !utf8.ValidString(id) || len(id) > 1400 {
		return "", false
	}
	return cursorEncodedCallIDPrefix + base64.RawURLEncoding.EncodeToString([]byte(id)), true
}
