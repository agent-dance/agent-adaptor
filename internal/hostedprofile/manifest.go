package hostedprofile

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/profile"
)

const markerLimit = 64 << 10

type namespaceRecord struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	SourceDir string `json:"source_dir"`
	SourceID  string `json:"source_id"`
}
type ownerRecord struct {
	Format       string `json:"format"`
	Version      int    `json:"version"`
	KeyHash      string `json:"key_hash"`
	DriverType   string `json:"driver_type"`
	SourceDir    string `json:"source_dir"`
	SourceID     string `json:"source_id"`
	IdentityHash string `json:"identity_hash"`
}
type stateRecord struct {
	Version    int    `json:"version"`
	Phase      string `json:"phase"`
	Generation string `json:"generation"`
}
type seedOwner struct {
	Version    int    `json:"version"`
	KeyHash    string `json:"key_hash"`
	Generation string `json:"generation"`
}

func hashFields(fields ...string) string {
	h := sha256.New()
	var b [8]byte
	for _, s := range fields {
		binary.BigEndian.PutUint64(b[:], uint64(len(s)))
		h.Write(b[:])
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func hashBytes(raw []byte) string { b := sha256.Sum256(raw); return hex.EncodeToString(b[:]) }
func generation() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func validHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func unsafe(stage string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", profile.ErrUnsafe, stage)
	}
	return fmt.Errorf("%w: %s: %w", profile.ErrUnsafe, stage, err)
}

// DecodeJSON rejects duplicate keys and trailing values before typed decoding.
// With exact=true all struct fields are required and unknown fields are rejected.
func DecodeJSON(raw []byte, out any, exact bool) error {
	if !utf8.Valid(raw) {
		return unsafe("invalid control UTF-8", nil)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := checkJSONValue(d); err != nil {
		return unsafe("invalid JSON", err)
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return unsafe("trailing JSON", err)
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if exact {
		d.DisallowUnknownFields()
	}
	if err := d.Decode(out); err != nil {
		return unsafe("invalid control record", err)
	}
	if exact {
		want, err := json.Marshal(out)
		if err != nil {
			return err
		}
		var expected, actual any
		if json.Unmarshal(want, &expected) != nil || json.Unmarshal(raw, &actual) != nil || !sameJSONShape(expected, actual) {
			return unsafe("missing or invalid control fields", nil)
		}

	}
	return nil
}
func checkJSONValue(d *json.Decoder) error {
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("duplicate or invalid object key")
			}
			seen[s] = true
			if err := checkJSONValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := checkJSONValue(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}
func (s stateRecord) valid() bool {
	return s.Version == 1 && ((s.Phase == "unseeded" || s.Phase == "ready") && s.Generation == "" || s.Phase == "active" && validHex(s.Generation, 32))
}

func sameJSONShape(expected, actual any) bool {
	switch e := expected.(type) {
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok || len(e) != len(a) {
			return false
		}
		for key, v := range e {
			other, ok := a[key]
			if !ok || !sameJSONShape(v, other) {
				return false
			}
		}
	case []any:
		a, ok := actual.([]any)
		if !ok || len(e) != len(a) {
			return false
		}
		for i, v := range e {
			if !sameJSONShape(v, a[i]) {
				return false
			}
		}
	}
	return true
}
