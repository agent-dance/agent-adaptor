// Package profilejson reconciles Driver-rendered JSON/JSONC resource entries.
// It owns file transactions and ownership, never provider protocol semantics.
package profilejson

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilekind"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/tailscale/hujson"
)

type Layout struct {
	Provider, Root, File, Field string
	Kind                        driver.ProfileResourceKind
	Materialization             driver.ProfileResourceMaterialization
	Metadata                    map[string]map[string]string
	// Ordered preserves nested object order for provider formats whose rules
	// use insertion precedence. Ownership then includes that order.
	Ordered bool
}

type State struct {
	Entries  map[string]json.RawMessage
	Managed  map[string]bool
	Owners   map[string]profilestate.ManifestEntry
	Snapshot driver.ResourceSnapshot
}

type document struct {
	value  hujson.Value
	raw    []byte
	exists bool
	mode   os.FileMode
}

func (l Layout) validate() error {
	if strings.TrimSpace(l.Root) == "" || l.Provider == "" || l.Field == "" || l.Kind == "" || l.File == "" || l.File == "." || l.File == ".." || strings.ContainsAny(l.File, `/\:`) {
		return errors.New("profile JSON requires provider, resource kind, field and a direct native filename")
	}
	return nil
}

// Observe is read-only, including when the profile directory does not exist.
func Observe(l Layout, desired map[string]json.RawMessage, fingerprint string) (State, error) {
	if err := l.validate(); err != nil {
		return State{}, err
	}
	desired, err := canonicalEntries(desired, l.Ordered)
	if err != nil {
		return State{}, err
	}
	manifest, err := profilestate.LoadManifest(l.Root)
	if err != nil {
		return State{}, err
	}
	doc, err := readDocument(filepath.Join(l.Root, l.File))
	if err != nil {
		return State{}, err
	}
	return observe(l, doc, manifest, desired, fingerprint)
}

func observe(l Layout, doc document, manifest profilestate.Manifest, desired map[string]json.RawMessage, fingerprint string) (State, error) {
	entries, err := section(doc.value, l.Field, l.Ordered)
	if err != nil {
		return State{}, err
	}
	out := State{Entries: entries, Managed: map[string]bool{}, Owners: map[string]profilestate.ManifestEntry{}, Snapshot: driver.ResourceSnapshot{Kind: l.Kind, Fingerprint: fingerprint, Support: driver.ProfileResourceSupportPortableCore, Materialization: driver.ProfileResourceMaterializationNotMaterialized}}
	for key, raw := range entries {
		entry, ok := manifest.Entry(string(l.Kind), key)
		if ok && entry.Metadata["provider"] == l.Provider && entry.Metadata["field"] == l.Field && profilekind.SamePath(entry.Path, filepath.Join(l.Root, l.File)) && entry.Metadata["rendered_sha256"] == hash(raw) {
			out.Managed[key] = true
			out.Owners[key] = entry
			out.Snapshot.Managed = append(out.Snapshot.Managed, key)
		} else {
			out.Snapshot.External = append(out.Snapshot.External, key)
		}
	}
	if len(out.Managed) > 0 {
		out.Snapshot.Materialization = l.Materialization
	}
	for key, raw := range desired {
		matches := out.Managed[key] && hash(raw) == hash(entries[key])
		for k, v := range l.Metadata[key] {
			matches = matches && out.Owners[key].Metadata[k] == v
		}
		if !matches {
			out.Snapshot.Warnings = append(out.Snapshot.Warnings, fmt.Sprintf("Desired %s entry %q is not installed with matching ownership and content.", l.Kind, key))
		}
	}
	sort.Strings(out.Snapshot.Managed)
	sort.Strings(out.Snapshot.External)
	sort.Strings(out.Snapshot.Warnings)
	return out, nil
}

// Sync replaces this resource family's SDK-owned set, preserving external
// entries and unrelated config syntax. The manifest is the commit point.
// Failed commits restore the original bytes unless an external writer changed
// the file in the meantime. Call only for an explicitly declared family.
func Sync(ctx context.Context, l Layout, desired map[string]json.RawMessage, fingerprint string) (out State, err error) {
	if err = l.validate(); err != nil {
		return out, err
	}
	for key, raw := range desired {
		if strings.TrimSpace(key) == "" || key != strings.TrimSpace(key) || !json.Valid(raw) {
			return out, errors.New("profile JSON requires nonempty exact resource keys and valid JSON values")
		}
	}
	desired, err = canonicalEntries(desired, l.Ordered)
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	lock, err := profilestate.AcquireLock(ctx, l.Root, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	manifest, err := profilestate.LoadManifest(l.Root)
	if err != nil {
		return out, err
	}
	path := filepath.Join(l.Root, l.File)
	doc, err := readDocument(path)
	if err != nil {
		return out, err
	}
	state, err := observe(l, doc, manifest, nil, fingerprint)
	if err != nil {
		return out, err
	}
	owned := map[string]bool{}
	for _, entry := range manifest.KindEntries(string(l.Kind)) {
		if entry.Metadata["provider"] != l.Provider {
			continue
		}
		if !profilekind.SamePath(entry.Path, path) || entry.Metadata["field"] != l.Field {
			return out, errors.New("profile JSON manifest points outside the native resource field")
		}
		owned[entry.Key] = true
		if _, exists := state.Entries[entry.Key]; exists && !state.Managed[entry.Key] {
			return out, fmt.Errorf("%s: managed %s entry %q was externally modified", l.Provider, l.Kind, entry.Key)
		}
	}
	for key := range desired {
		if entry, exists := manifest.Entry(string(l.Kind), key); exists && entry.Metadata["provider"] != l.Provider {
			return out, fmt.Errorf("%s entry %q belongs to another provider", l.Kind, key)
		}
		if _, exists := state.Entries[key]; exists && !state.Managed[key] {
			return out, fmt.Errorf("%s entry %q is external; refusing to overwrite it", l.Kind, key)
		}
	}
	if len(desired) == 0 && len(owned) == 0 {
		return state, nil
	}
	var patches []map[string]any
	field := "/" + pointer(l.Field)
	if doc.value.Find(field) == nil && len(desired) > 0 {
		patches = append(patches, map[string]any{"op": "add", "path": field, "value": map[string]any{}})
	}
	var keys []string
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := desired[key]
		if !bytes.Equal(raw, state.Entries[key]) {
			patches = append(patches, map[string]any{"op": "add", "path": field + "/" + pointer(key), "value": raw})
		}
		metadata := map[string]string{}
		for k, v := range l.Metadata[key] {
			metadata[k] = v
		}
		metadata["provider"], metadata["field"], metadata["rendered_sha256"] = l.Provider, l.Field, hash(raw)
		manifest.Set(profilestate.ManifestEntry{Kind: string(l.Kind), Key: key, Path: path, Fingerprint: hash(raw), Metadata: metadata})
	}
	for key := range owned {
		if _, keep := desired[key]; keep {
			continue
		}
		if _, exists := state.Entries[key]; exists {
			patches = append(patches, map[string]any{"op": "remove", "path": field + "/" + pointer(key)})
		}
		manifest.Remove(string(l.Kind), key)
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	var written []byte
	committed := false
	defer func() {
		if err == nil || committed || written == nil {
			return
		}
		current, readErr := readDocument(path)
		if readErr != nil || !current.exists || !bytes.Equal(current.raw, written) {
			err = errors.Join(err, fmt.Errorf("profile JSON rollback refused: native file changed externally"))
			return
		}
		if doc.exists {
			err = errors.Join(err, profilestate.AtomicWriteFile(path, doc.raw, doc.mode))
		} else {
			err = errors.Join(err, os.Remove(path))
		}
	}()
	if len(patches) > 0 {
		raw, _ := json.Marshal(patches)
		if err = doc.value.Patch(raw); err != nil {
			return out, err
		}
		written = doc.value.Pack()
		current, readErr := readDocument(path)
		if readErr != nil || current.exists != doc.exists || !bytes.Equal(current.raw, doc.raw) {
			written = nil
			return out, errors.Join(readErr, errors.New("native profile JSON changed during synchronization"))
		}
		mode := doc.mode
		if !doc.exists {
			mode = 0600
		}
		if err = profilestate.AtomicWriteFile(path, written, mode); err != nil {
			written = nil
			return out, err
		}
	}
	if err = profilestate.SaveManifest(l.Root, manifest); err != nil {
		return out, err
	}
	committed = true
	return Observe(l, desired, fingerprint)
}

// ReadEntries reads a Driver-chosen native field without loading ownership.
func ReadEntries(root, file, field string, ordered ...bool) (map[string]json.RawMessage, error) {
	l := Layout{Root: root, File: file, Field: field, Provider: "read", Kind: "read"}
	if err := l.validate(); err != nil {
		return nil, err
	}
	doc, err := readDocument(filepath.Join(root, file))
	if err != nil {
		return nil, err
	}
	return section(doc.value, field, ordered...)
}

func readDocument(path string) (document, error) {
	d := document{}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		d.value, _ = hujson.Parse([]byte("{}\n"))
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if !info.Mode().IsRegular() {
		return d, fmt.Errorf("profile JSON requires a regular native config file: %s", path)
	}
	d.exists, d.mode = true, info.Mode().Perm()
	d.raw, err = os.ReadFile(path)
	if err != nil {
		return d, err
	}
	d.value, err = hujson.Parse(d.raw)
	if err != nil {
		return d, fmt.Errorf("parse native profile JSON: %w", err)
	}
	if _, ok := d.value.Value.(*hujson.Object); !ok {
		return d, errors.New("native profile JSON must be an object")
	}
	for value := range d.value.All() {
		if obj, ok := value.Value.(*hujson.Object); ok {
			seen := map[string]bool{}
			for _, member := range obj.Members {
				name := member.Name.Value.(hujson.Literal).String()
				if seen[name] {
					return d, errors.New("native profile JSON contains duplicate object keys")
				}
				seen[name] = true
			}
		}
	}
	return d, nil
}

func section(value hujson.Value, field string, ordered ...bool) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	v := value.Find("/" + pointer(field))
	if v == nil {
		return out, nil
	}
	obj, ok := v.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("native profile field %q must be an object", field)
	}
	for _, member := range obj.Members {
		v := member.Value.Clone()
		out[member.Name.Value.(hujson.Literal).String()] = canonicalValue(v, ordered...)
	}
	return out, nil
}
func canonicalEntries(entries map[string]json.RawMessage, ordered ...bool) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(entries))
	for key, raw := range entries {
		if !json.Valid(raw) {
			return nil, errors.New("profile resource value must be valid JSON")
		}
		value, err := hujson.Parse(raw)
		if err != nil {
			return nil, err
		}
		out[key] = canonicalValue(value, ordered...)
	}
	return out, nil
}

// Normalize layout and object order while preserving literal spellings. Some
// native formats expand templates before JSON decoding, so changing an escaped
// string into its unescaped equivalent can change execution semantics.
func canonicalValue(value hujson.Value, ordered ...bool) []byte {
	for child := range value.All() {
		if obj, ok := child.Value.(*hujson.Object); ok && !(len(ordered) > 0 && ordered[0]) {
			sort.Slice(obj.Members, func(i, j int) bool {
				return obj.Members[i].Name.Value.(hujson.Literal).String() < obj.Members[j].Name.Value.(hujson.Literal).String()
			})
		}
	}
	value.Standardize()
	value.Minimize()
	return value.Pack()
}
func pointer(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }
func hash(raw []byte) string  { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
