package engine

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// SkillFromPath sources a skill from an existing local directory that
// contains SKILL.md (and optional references).
type SkillFromPath struct {
	Path string
}

// SkillSource implements [SkillSource].
func (SkillFromPath) SkillSource() {}

// SkillPath is the source projection consumed by the materializer. Keeping
// the projection structural lets public leaf packages own their concrete
// source types without creating an engine import cycle.
func (s SkillFromPath) SkillPath() string { return s.Path }

// SkillFromFS sources a skill from an io/fs.FS tree rooted at Root. The root
// entry must contain SKILL.md. Root == "" or "." is equivalent.
type SkillFromFS struct {
	FS   fs.FS
	Root string
}

// SkillSource implements [SkillSource].
func (SkillFromFS) SkillSource() {}

// SkillFS is the source projection consumed by the materializer.
func (s SkillFromFS) SkillFS() (fs.FS, string) { return s.FS, s.Root }

// SkillFromInline carries a single SKILL.md string. Callers that need
// auxiliary reference files should use SkillFromFS instead.
type SkillFromInline struct {
	SkillMD string
}

// SkillSource implements [SkillSource].
func (SkillFromInline) SkillSource() {}

// InlineSkillMD is the source projection consumed by the materializer.
func (s SkillFromInline) InlineSkillMD() string { return s.SkillMD }

// Key is the idiomatic constructor for a SkillRef referring to a provider
// key. It is equivalent to converting the string to SkillKey directly.
func Key(k string) SkillRef { return SkillKey(k) }

// LocalSkill builds a Skill sourced from a local directory. Key defaults to
// the directory basename; callers may override it by assigning to the
// returned Skill's Key field.
func LocalSkill(dir string) Skill {
	return Skill{Key: keyFromPath(dir), Source: SkillFromPath{Path: dir}}
}

// FSSkill builds a Skill sourced from a fs.FS sub-tree rooted at root.
func FSSkill(f fs.FS, root string) Skill {
	key := keyFromPath(root)
	if key == "" {
		key = "skill"
	}
	return Skill{Key: key, Source: SkillFromFS{FS: f, Root: root}}
}

// InlineSkill builds a Skill whose entire content is the given SKILL.md
// string. Key is required.
func InlineSkill(key, skillMD string) Skill {
	return Skill{Key: key, Source: SkillFromInline{SkillMD: skillMD}}
}

// Require returns a copy of s marked Required=true with the given reason.
func Require(s Skill, reason string) Skill {
	s.Required = true
	s.Reason = reason
	return s
}

// SkillProvider is the host-side hook that backs WithSkills /
// WithDefaultSkills. The SDK consolidates and deduplicates the
// SkillKeys referenced by one Run, then asks the provider to produce
// a Skill description for each key.
//
// Implementations MAY return additional Skill values not in keys (for
// example tenant-mandatory skills); SDK adds every returned Skill to
// the run's selected set, treating provider-injected additions
// equivalently to user-referenced keys. Keys absent from the returned
// map surface as ErrSkillNotFound.
//
// SDK invokes GetSkills on every Run regardless of whether the caller
// referenced any keys, so provider-injected mandatory skills work
// even when the user passed neither WithSkills nor WithDefaultSkills.
//
// Caller scoping (tenant / profile) is propagated through the ctx;
// providers extract it with [CallerIdentityFromContext]. This keeps
// the interface signature minimal even when a host's catalogue is
// partitioned by deployment-specific dimensions.
//
// Hosts whose catalogue can be enumerated in full (admin UIs) should
// also implement [SkillCatalog]; SDK detects the extension via type
// assertion. Hosts whose catalogue is too large to enumerate (remote
// stores, etc.) implement only SkillProvider.
type SkillProvider interface {
	GetSkills(ctx context.Context, keys []string) (map[string]Skill, error)
}

// SkillCatalog extends SkillProvider with the ability to enumerate
// the full visible catalogue. SDK uses Catalogue() only for
// Admin.ListSkills; Run-time selection still goes through GetSkills
// regardless.
//
// Hosts whose catalogue is too large to enumerate (e.g. remote skill
// stores with thousands of entries) implement only SkillProvider and
// skip SkillCatalog. SDK reports SkillSyncMode = SkillSyncUnsupported
// on Admin.ListSkills in that case, leaving admin discovery to the
// host's own UI (which the store typically already provides).
type SkillCatalog interface {
	SkillProvider
	Catalogue(ctx context.Context) ([]Skill, error)
}

// SkillSet is a static map-based SkillCatalog convenient for hosts
// whose skill catalogue is known at construction time. It satisfies
// both SkillProvider (GetSkills) and SkillCatalog (Catalogue), so
// passing one to WithSkillProvider also enables Admin.ListSkills.
//
// Skills with the empty Key are normalised to the map key on access,
// which lets hosts write
//
//	sdk.SkillSet{"foo": {Source: ...}}
//
// without repeating "foo" inside the value.
type SkillSet map[string]Skill

// GetSkills satisfies SkillProvider.
//
// Returned map keys are the canonical Skill.Key for each entry. Skills
// flagged Required are always included (regardless of whether they
// appear in keys). User-referenced keys absent from the catalogue do
// NOT generate an error here — the SDK reports them as ErrSkillNotFound
// in the resolution layer after merging all sources.
func (s SkillSet) GetSkills(_ context.Context, keys []string) (map[string]Skill, error) {
	out := make(map[string]Skill, len(keys))
	requested := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		requested[k] = struct{}{}
		if skill, ok := s.lookup(k); ok {
			out[skill.Key] = skill
		}
	}
	// Required entries are always part of the run's skill set.
	for mapKey, skill := range s {
		if !skill.Required {
			continue
		}
		canonical := s.canonicalKey(mapKey, skill)
		if _, already := out[canonical]; already {
			continue
		}
		if strings.TrimSpace(skill.Key) == "" {
			skill.Key = canonical
		}
		out[canonical] = skill
	}
	return out, nil
}

// Catalogue satisfies SkillCatalog.
func (s SkillSet) Catalogue(_ context.Context) ([]Skill, error) {
	out := make([]Skill, 0, len(s))
	for mapKey, skill := range s {
		if strings.TrimSpace(skill.Key) == "" {
			skill.Key = mapKey
		}
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// lookup resolves an explicit key against the SkillSet, returning the
// associated Skill (with Key normalised when the value left it empty).
func (s SkillSet) lookup(key string) (Skill, bool) {
	skill, ok := s[key]
	if !ok {
		return Skill{}, false
	}
	if strings.TrimSpace(skill.Key) == "" {
		skill.Key = key
	}
	return skill, true
}

// canonicalKey returns the Key SDK uses for the given (mapKey, skill)
// pair: the explicit Skill.Key if non-empty, the map key otherwise.
func (s SkillSet) canonicalKey(mapKey string, skill Skill) string {
	if k := strings.TrimSpace(skill.Key); k != "" {
		return k
	}
	return mapKey
}

// SkillMaterializer lets hosts customise how SkillFromFS / SkillFromInline
// sources are written to disk before an adapter consumes them. The returned
// sourcePath must be a directory containing SKILL.md.
//
// Implementations are responsible for caching, atomic writes, and any
// host-specific isolation (for example multi-tenant cache roots). The SDK
// default implementation is documented in docs/skill-api-design.md §3.
type SkillMaterializer interface {
	Materialize(ctx context.Context, s Skill) (sourcePath string, err error)
}

// SkillKeyConflictError is returned (wrapped by ErrSkillKeyConflict) when
// two skill candidates share the same Key but differ structurally. The
// Sources slice lists human-readable labels for the conflicting origins
// (for example "binding:default", "run:per-call", "provider").
type SkillKeyConflictError struct {
	Key     string
	Sources []string
	Detail  string
}

// Error returns a diagnostic message that includes the offending Key and
// the contributing sources.
func (e *SkillKeyConflictError) Error() string {
	if e == nil {
		return ErrSkillKeyConflict.Error()
	}
	parts := append([]string(nil), e.Sources...)
	sort.Strings(parts)
	msg := fmt.Sprintf("agentadaptor: skill key %q is defined by multiple sources with different content", e.Key)
	if len(parts) > 0 {
		msg += " [" + strings.Join(parts, ", ") + "]"
	}
	if strings.TrimSpace(e.Detail) != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Unwrap lets callers detect the error kind with errors.Is(err, ErrSkillKeyConflict).
func (e *SkillKeyConflictError) Unwrap() error { return ErrSkillKeyConflict }

// SkillMaterializationError is returned (matching
// ErrSkillMaterializationFailed) when a selected skill is known to the SDK but
// cannot be materialized into a local SKILL.md directory before adapter.Run.
type SkillMaterializationError struct {
	Key         string
	RuntimeName string
	Cause       error
}

func (e *SkillMaterializationError) Error() string {
	if e == nil {
		return ErrSkillMaterializationFailed.Error()
	}
	msg := fmt.Sprintf("agentadaptor: skill %q materialization failed", e.Key)
	if strings.TrimSpace(e.RuntimeName) != "" && e.RuntimeName != e.Key {
		msg += fmt.Sprintf(" (runtime name %q)", e.RuntimeName)
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap exposes the underlying materializer error, so hosts can still match
// lower-level causes such as ErrSkillSourceMissing where appropriate.
func (e *SkillMaterializationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is lets errors.Is match ErrSkillMaterializationFailed while Unwrap still
// exposes Cause for lower-level checks.
func (e *SkillMaterializationError) Is(target error) bool {
	return target == ErrSkillMaterializationFailed
}

// keyFromPath returns the slug-ish basename of a path-like string. The
// rules intentionally mirror the filesystem basename of a skill directory so
// that LocalSkill and FSSkill pick friendly defaults.
func keyFromPath(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == "" {
		return ""
	}
	base := path.Base(trimmed)
	switch base {
	case ".", "/", "":
		return ""
	}
	return base
}
