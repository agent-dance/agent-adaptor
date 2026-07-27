package sessionrecorder

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// HostSeq is the host-scoped cursor assigned to each recorded Event. It is
// strictly monotonic within one session key and remains stable across SDK run
// boundaries.
type HostSeq = uint64

// SessionInfo summarizes one recorded session for recent-session listings.
type SessionInfo struct {
	Key        string    `json:"key"`
	LastSeq    HostSeq   `json:"last_seq"`
	RecordedAt time.Time `json:"recorded_at"`
}

// KeyValidator returns an error when a session key is not accepted. A
// validator must be deterministic and side-effect-free.
type KeyValidator func(sessionKey string) error

// DefaultKeyPattern matches portable single-component session keys: an
// alphanumeric leader followed by at most 127 alphanumerics, dashes, or
// underscores.
var DefaultKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_\-]{0,127}$`)

// DefaultKeyValidator applies DefaultKeyPattern. JSONLEventBackend performs
// an additional non-replaceable filesystem-containment check.
var DefaultKeyValidator KeyValidator = func(key string) error {
	if !DefaultKeyPattern.MatchString(key) {
		return fmt.Errorf("sessionrecorder: refused session key %q: must match %s", key, DefaultKeyPattern)
	}
	return nil
}

// ErrInvalidSessionKey identifies a key rejected by the recorder or its
// backend. The validator's diagnostic is included in the returned error.
var ErrInvalidSessionKey = errors.New("sessionrecorder: invalid session key")

func defaultClock() time.Time { return time.Now().UTC() }
