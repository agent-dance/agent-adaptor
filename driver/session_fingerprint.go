package driver

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
)

// SessionConfigFingerprinter is the stable construction-config identity
// contract used when a Driver participates in a resumable Thread.
//
// A resume-capable Driver used by Thread MUST implement this interface. The
// returned fingerprint MUST be non-empty, deterministic across processes, and
// cover every construction-time value visible to the provider as well as the
// Driver's session-codec/version contract. Implementations MUST return an
// error when they cannot represent a value stably; silently omitting such a
// value can resume a session under an incompatible configuration.
//
// The fingerprint is an opaque compatibility token. Callers MUST NOT parse it
// or expose it as configuration, and implementations MUST NOT return raw
// configuration or secret values in errors.
type SessionConfigFingerprinter interface {
	SessionConfigFingerprint() (string, error)
}

// SessionConfigFingerprintError reports that construction config cannot be
// represented by the strict canonical encoder. Path contains field names and
// generic collection positions only; it never contains map keys or values.
// Type and Kind describe the rejected Go shape without formatting its value.
type SessionConfigFingerprintError struct {
	Path string
	Type string
	Kind reflect.Kind
	Why  string
}

func (e *SessionConfigFingerprintError) Error() string {
	if e == nil {
		return "session config fingerprint error"
	}
	msg := "session config cannot be fingerprinted"
	if e.Path != "" {
		msg += " at " + e.Path
	}
	if e.Type != "" {
		msg += " (type " + e.Type + ")"
	}
	if e.Kind != reflect.Invalid {
		msg += ": unsupported " + e.Kind.String()
	}
	if e.Why != "" {
		msg += ": " + e.Why
	}
	return msg
}

// CanonicalSessionConfigFingerprint returns a SHA-256 compatibility token for
// value under a caller-owned version domain. The domain should identify the
// Driver and session-codec contract (for example, "acme/v2;codec/v1") and
// must change whenever the meaning of the encoded configuration changes.
//
// Canonicalization is deliberately strict:
//   - map insertion order is ignored and map entries are sorted by encoded key;
//   - nil and empty maps/slices are equivalent because both mean "no values";
//   - nil pointers/interfaces remain distinct from present zero values;
//   - pointers are dereferenced and their addresses are never encoded;
//   - funcs, channels, unsafe pointers, uintptrs, cycles, and structs with
//     unexported state are rejected rather than guessed;
//   - errors describe shape only and never include a field value or map key.
//
// All exported struct fields, including zero-valued fields, participate. This
// makes newly-added provider Config fields fail closed by changing the token.
func CanonicalSessionConfigFingerprint(domain string, value any) (string, error) {
	if strings.TrimSpace(domain) == "" {
		return "", errors.New("session config fingerprint domain is empty")
	}
	var canonical bytes.Buffer
	enc := canonicalConfigEncoder{
		dst:      &canonical,
		visiting: make(map[canonicalVisit]struct{}),
	}
	enc.string("agent-adaptor/session-config/v1")
	enc.string(domain)
	if err := enc.value(reflect.ValueOf(value), "$config"); err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type canonicalVisit struct {
	typ reflect.Type
	ptr uintptr
}

type canonicalMapEntry struct {
	key      []byte
	mapKey   reflect.Value
	mapValue reflect.Value
}

type canonicalConfigEncoder struct {
	dst      *bytes.Buffer
	visiting map[canonicalVisit]struct{}
}

func (e *canonicalConfigEncoder) value(v reflect.Value, path string) error {
	if !v.IsValid() {
		e.byte('0')
		return nil
	}

	e.byte('T')
	e.string(canonicalTypeName(v.Type()))
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			e.byte('0')
			return nil
		}
		e.byte('1')
		return e.value(v.Elem(), path)
	case reflect.Pointer:
		if v.IsNil() {
			e.byte('0')
			return nil
		}
		e.byte('1')
		return e.withVisit(v, path, func() error { return e.value(v.Elem(), path+"*") })
	case reflect.Bool:
		if v.Bool() {
			e.byte('1')
		} else {
			e.byte('0')
		}
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.uint64(uint64(v.Int()))
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		e.uint64(v.Uint())
		return nil
	case reflect.Float32:
		e.uint64(uint64(math.Float32bits(float32(v.Float()))))
		return nil
	case reflect.Float64:
		e.uint64(math.Float64bits(v.Float()))
		return nil
	case reflect.Complex64:
		value := complex64(v.Complex())
		e.uint64(uint64(math.Float32bits(real(value))))
		e.uint64(uint64(math.Float32bits(imag(value))))
		return nil
	case reflect.Complex128:
		value := v.Complex()
		e.uint64(math.Float64bits(real(value)))
		e.uint64(math.Float64bits(imag(value)))
		return nil
	case reflect.String:
		e.string(v.String())
		return nil
	case reflect.Array:
		e.uint64(uint64(v.Len()))
		for i := 0; i < v.Len(); i++ {
			if err := e.value(v.Index(i), path+"[element]"); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		// nil and empty slices deliberately share this representation.
		e.uint64(uint64(v.Len()))
		if v.Len() == 0 {
			return nil
		}
		return e.withVisit(v, path, func() error {
			for i := 0; i < v.Len(); i++ {
				if err := e.value(v.Index(i), path+"[element]"); err != nil {
					return err
				}
			}
			return nil
		})
	case reflect.Map:
		// nil and empty maps deliberately share this representation.
		e.uint64(uint64(v.Len()))
		if v.Len() == 0 {
			return nil
		}
		return e.withVisit(v, path, func() error { return e.mapValue(v, path) })
	case reflect.Struct:
		e.uint64(uint64(v.NumField()))
		for i := 0; i < v.NumField(); i++ {
			fieldInfo := v.Type().Field(i)
			fieldPath := path + "." + fieldInfo.Name
			if !fieldInfo.IsExported() {
				return unstableConfigError(fieldPath, v.Field(i), "struct contains unexported state")
			}
			e.string(fieldInfo.Name)
			if err := e.value(v.Field(i), fieldPath); err != nil {
				return err
			}
		}
		return nil
	case reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Uintptr:
		return unstableConfigError(path, v, "value has no stable cross-process representation")
	default:
		return unstableConfigError(path, v, "value kind is not supported")
	}
}

func (e *canonicalConfigEncoder) mapValue(v reflect.Value, path string) error {
	entries := make([]canonicalMapEntry, 0, v.Len())
	iter := v.MapRange()
	for iter.Next() {
		keyBytes, err := e.fragment(iter.Key(), path+"[key]")
		if err != nil {
			return err
		}
		entries = append(entries, canonicalMapEntry{
			key:      keyBytes,
			mapKey:   iter.Key(),
			mapValue: iter.Value(),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i].key, entries[j].key) < 0 })
	for i := range entries {
		if i > 0 && bytes.Equal(entries[i-1].key, entries[i].key) {
			return unstableConfigError(path+"[key]", entries[i].mapKey, "distinct map keys have the same canonical representation")
		}
		e.bytes(entries[i].key)
		if err := e.value(entries[i].mapValue, path+"[value]"); err != nil {
			return err
		}
	}
	return nil
}

func (e *canonicalConfigEncoder) fragment(v reflect.Value, path string) ([]byte, error) {
	var fragment bytes.Buffer
	child := canonicalConfigEncoder{dst: &fragment, visiting: e.visiting}
	if err := child.value(v, path); err != nil {
		return nil, err
	}
	return fragment.Bytes(), nil
}

func (e *canonicalConfigEncoder) withVisit(v reflect.Value, path string, fn func() error) error {
	visit := canonicalVisit{typ: v.Type(), ptr: v.Pointer()}
	if _, exists := e.visiting[visit]; exists {
		return unstableConfigError(path, v, "cyclic reference")
	}
	e.visiting[visit] = struct{}{}
	defer delete(e.visiting, visit)
	return fn()
}

func (e *canonicalConfigEncoder) byte(value byte) {
	_ = e.dst.WriteByte(value)
}

func (e *canonicalConfigEncoder) uint64(value uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], value)
	_, _ = e.dst.Write(buf[:])
}

func (e *canonicalConfigEncoder) string(value string) {
	e.bytes([]byte(value))
}

func (e *canonicalConfigEncoder) bytes(value []byte) {
	e.uint64(uint64(len(value)))
	_, _ = e.dst.Write(value)
}

func canonicalTypeName(t reflect.Type) string {
	if t.PkgPath() == "" {
		return t.String()
	}
	return t.PkgPath() + "." + t.Name()
}

func unstableConfigError(path string, value reflect.Value, why string) error {
	var typ string
	var kind reflect.Kind
	if value.IsValid() {
		typ = canonicalTypeName(value.Type())
		kind = value.Kind()
	}
	return &SessionConfigFingerprintError{Path: path, Type: typ, Kind: kind, Why: why}
}
