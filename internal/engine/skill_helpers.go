package engine

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// --- clone helpers --------------------------------------------------------

// cloneSkill produces a deep copy of a Skill value. Metadata is cloned but
// the Source interface is reused by value; SkillSource implementations are
// intentionally immutable so sharing them is safe.
func cloneSkill(s Skill) Skill {
	return Skill{
		Key:      s.Key,
		Source:   s.Source,
		Required: s.Required,
		Reason:   s.Reason,
		Metadata: cloneStringMap(s.Metadata),
	}
}

func cloneSkills(values []Skill) []Skill {
	if len(values) == 0 {
		return nil
	}
	out := make([]Skill, len(values))
	for i, v := range values {
		out[i] = cloneSkill(v)
	}
	return out
}

func cloneSkillRefs(values []SkillRef) []SkillRef {
	if len(values) == 0 {
		return nil
	}
	out := make([]SkillRef, 0, len(values))
	for _, v := range values {
		switch ref := v.(type) {
		case nil:
			continue
		case SkillKey:
			out = append(out, ref)
		case Skill:
			out = append(out, cloneSkill(ref))
		default:
			// The SkillRef sum-type is closed; we never expect to see an
			// out-of-family implementation in the SDK, but the defensive
			// branch keeps cloning total.
			out = append(out, v)
		}
	}
	return out
}

func cloneResolvedSkill(e ResolvedSkill) ResolvedSkill {
	return ResolvedSkill{
		Key:         e.Key,
		RuntimeName: e.RuntimeName,
		SourcePath:  e.SourcePath,
		Required:    e.Required,
		Reason:      e.Reason,
		Metadata:    cloneStringMap(e.Metadata),
	}
}

func cloneResolvedSkills(r ResolvedSkills) ResolvedSkills {
	entries := make([]ResolvedSkill, len(r.Entries))
	for i, entry := range r.Entries {
		entries[i] = cloneResolvedSkill(entry)
	}
	return ResolvedSkills{
		Mode:        r.Mode,
		Entries:     entries,
		Warnings:    cloneStrings(r.Warnings),
		Fingerprint: r.Fingerprint,
	}
}

func cloneSnapshotEntries(values []SnapshotEntry) []SnapshotEntry {
	if len(values) == 0 {
		return nil
	}
	out := make([]SnapshotEntry, len(values))
	copy(out, values)
	return out
}

// --- key / slug helpers ---------------------------------------------------

func normalizeSkillKey(value string) string {
	return strings.TrimSpace(value)
}

func skillSlug(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	parts := strings.Split(trimmed, "/")
	return parts[len(parts)-1]
}

// defaultSkillRuntimeName derives the runtime directory name used when the
// materializer writes a skill to disk. It prefers the Metadata override
// (SkillMetadataRuntimeName), falls back to the slugged Key, and finally to
// "skill" when the Key is empty.
func defaultSkillRuntimeName(s Skill) string {
	if override := strings.TrimSpace(s.Metadata[SkillMetadataRuntimeName]); override != "" {
		return override
	}
	slug := skillSlug(s.Key)
	if slug == "" {
		slug = "skill"
	}
	if strings.ToLower(strings.TrimSpace(s.Key)) == slug {
		return slug
	}
	// The slug differs from the original Key (e.g. "team/retention" vs
	// "retention"). Preserve uniqueness by suffixing a short content hash.
	return slug + "--" + stableHash(s.Key)[:10]
}

// --- skill equivalence -----------------------------------------------------

// skillsEquivalent reports whether two Skill values describe the same
// content (Key, Source, Required, Reason, Metadata). This is the invariant
// enforced by ErrSkillKeyConflict during additive merging.
func skillsEquivalent(a, b Skill) bool {
	if normalizeSkillKey(a.Key) != normalizeSkillKey(b.Key) {
		return false
	}
	if a.Required != b.Required {
		return false
	}
	if strings.TrimSpace(a.Reason) != strings.TrimSpace(b.Reason) {
		return false
	}
	if !stringMapsEqual(a.Metadata, b.Metadata) {
		return false
	}
	return skillSourcesEquivalent(a.Source, b.Source)
}

func skillSourcesEquivalent(a, b SkillSource) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch left := a.(type) {
	case skillPathProjection:
		right, ok := b.(skillPathProjection)
		if !ok {
			return false
		}
		return cleanSkillPath(left.SkillPath()) == cleanSkillPath(right.SkillPath())
	case skillFSProjection:
		right, ok := b.(skillFSProjection)
		if !ok {
			return false
		}
		leftFS, leftRoot := left.SkillFS()
		rightFS, rightRoot := right.SkillFS()
		if leftRoot != rightRoot {
			return false
		}
		// fs.FS identity is the most reliable comparison we can make without
		// recursive enumeration. Hosts that want content-level equivalence
		// should wrap their FS in a stable value type.
		return fsIdentityEqual(leftFS, rightFS)
	case inlineSkillProjection:
		right, ok := b.(inlineSkillProjection)
		if !ok {
			return false
		}
		return left.InlineSkillMD() == right.InlineSkillMD()
	case archiveSkillProjection:
		right, ok := b.(archiveSkillProjection)
		if !ok {
			return false
		}
		_, leftFormat, leftSubpath, leftFingerprint := left.SkillArchive()
		_, rightFormat, rightSubpath, rightFingerprint := right.SkillArchive()
		if leftFormat != rightFormat {
			return false
		}
		// Subpath is compared in the form the materializer actually uses,
		// so "./docs" and "docs" describe the same skill.
		if normalizeSubpath(leftSubpath) != normalizeSubpath(rightSubpath) {
			return false
		}
		leftPrint := strings.TrimSpace(leftFingerprint)
		if leftPrint != strings.TrimSpace(rightFingerprint) {
			return false
		}
		// A non-empty Fingerprint is the host's declared identity for the
		// archive content, so matching fingerprints settle equivalence on
		// their own (the two openers may legitimately be a URL and a local
		// file serving the same bytes).
		if leftPrint != "" {
			return true
		}
		// Function values have no portable, stable content identity. In
		// particular, closures created by the same constructor can capture
		// different bytes while sharing one code address. Independent
		// declarations therefore require an explicit stable fingerprint.
		return false
	}
	return false
}

func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}

// fsIdentityEqual reports whether two fs.FS values can be treated as the
// same logical filesystem for the purpose of skill equivalence.
//
// The check is deliberately conservative:
//
//   - Both nil → equal.
//   - One nil → not equal.
//   - Different dynamic types → not equal.
//   - Same dynamic type that is comparable → use Go's `==` on the
//     interface values. This covers the common cases (pointer identity
//     for *fstest.MapFS, embed.FS values, small named structs used as
//     fs.FS adapters) without the cost or surprise of reflect.DeepEqual.
//   - Same dynamic type that is NOT comparable but has an addressable
//     data pointer (map, slice, chan, func, pointer) → compare the
//     underlying data pointer. Two references to the same map / slice
//     header are logically the same FS; two independent allocations are
//     not. This keeps fstest.MapFS and similar test helpers usable as
//     skill sources without forcing hosts to wrap them.
//   - Anything else (e.g. a non-comparable struct value) → not equal.
//     Hosts that need content equivalence should hand the SDK the same
//     FS instance twice, or wrap their FS in a comparable named type.
func fsIdentityEqual(a, b fs.FS) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta := reflect.TypeOf(a)
	tb := reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if ta == nil {
		return false
	}
	if ta.Comparable() {
		return a == b
	}
	switch ta.Kind() {
	case reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Pointer, reflect.UnsafePointer:
		return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
	default:
		return false
	}
}

func cleanSkillPath(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return filepath.Clean(trimmed)
	}
	return filepath.Clean(abs)
}

// skillDiffDetail renders a short human-readable diff between two Skills.
func skillDiffDetail(a, b Skill) string {
	diffs := make([]string, 0, 4)
	if a.Required != b.Required {
		diffs = append(diffs, fmt.Sprintf("Required %v vs %v", a.Required, b.Required))
	}
	if strings.TrimSpace(a.Reason) != strings.TrimSpace(b.Reason) {
		diffs = append(diffs, fmt.Sprintf("Reason %q vs %q", a.Reason, b.Reason))
	}
	if !stringMapsEqual(a.Metadata, b.Metadata) {
		diffs = append(diffs, "Metadata differs")
	}
	if !skillSourcesEquivalent(a.Source, b.Source) {
		diffs = append(diffs, fmt.Sprintf("Source %T vs %T", a.Source, b.Source))
	}
	return strings.Join(diffs, "; ")
}

// sortSkills returns a copy of skills sorted by Key for deterministic
// reporting / hashing.
func sortSkills(skills []Skill) []Skill {
	out := cloneSkills(skills)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
