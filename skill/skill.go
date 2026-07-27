package skill

import (
	"context"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Skill, Ref, and Source are the driver-facing value types exchanged at the
// core/SPI boundary. The host-side extension contracts below are deliberately
// declared in this package so application code never depends on internal
// engine types.
type (
	// Skill is the full description of one skill: identity (Key),
	// origin (Source), Required marker, human-readable Reason, and
	// optional Metadata. Skill values act as [Ref], so constructor
	// results can be passed straight to WithSkills /
	// WithDefaultSkills.
	Skill = driver.Skill

	// Ref is what WithSkills / WithDefaultSkills accept: either a
	// provider catalogue key (built with [Key]) or a fully-defined
	// [Skill] value (built with [Dir], [FS], [Inline], or [Archive]).
	Ref = driver.SkillRef

	// Source is the open marker interface for a Skill's origin.
	// Hosts may define custom Source types as long as a matching
	// [Materializer] is installed to handle them.
	Source = driver.SkillSource
)

// PathSource sources a skill from a local directory containing SKILL.md.
type PathSource struct{ Path string }

// SkillSource implements [Source].
func (PathSource) SkillSource() {}

// SkillPath exposes the source to the SDK's materialization boundary without
// coupling this public package to the internal engine.
func (s PathSource) SkillPath() string { return s.Path }

// FSSource sources a skill from an io/fs.FS tree rooted at Root.
type FSSource struct {
	FS   fs.FS
	Root string
}

// SkillSource implements [Source].
func (FSSource) SkillSource() {}

// SkillFS exposes the source to the SDK's materialization boundary.
func (s FSSource) SkillFS() (fs.FS, string) { return s.FS, s.Root }

// InlineSource carries a single SKILL.md document.
type InlineSource struct{ SkillMD string }

// SkillSource implements [Source].
func (InlineSource) SkillSource() {}

// InlineSkillMD exposes the source to the SDK's materialization boundary.
func (s InlineSource) InlineSkillMD() string { return s.SkillMD }

// Provider resolves catalogue keys into concrete skills for a run. Providers
// may additionally return Required skills that were not explicitly requested.
type Provider interface {
	GetSkills(ctx context.Context, keys []string) (map[string]Skill, error)
}

// Catalog extends [Provider] with deterministic catalogue enumeration.
type Catalog interface {
	Provider
	Catalogue(ctx context.Context) ([]Skill, error)
}

// Set is a static map-backed [Catalog].
type Set map[string]Skill

// GetSkills implements [Provider]. Required entries are always returned.
func (s Set) GetSkills(_ context.Context, keys []string) (map[string]Skill, error) {
	out := make(map[string]Skill, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if value, ok := s.lookup(key); ok {
			out[value.Key] = value
		}
	}
	for mapKey, value := range s {
		if !value.Required {
			continue
		}
		if strings.TrimSpace(value.Key) == "" {
			value.Key = mapKey
		}
		if _, exists := out[value.Key]; !exists {
			out[value.Key] = value
		}
	}
	return out, nil
}

// Catalogue implements [Catalog] and returns entries ordered by key.
func (s Set) Catalogue(_ context.Context) ([]Skill, error) {
	out := make([]Skill, 0, len(s))
	for mapKey, value := range s {
		if strings.TrimSpace(value.Key) == "" {
			value.Key = mapKey
		}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s Set) lookup(key string) (Skill, bool) {
	value, ok := s[key]
	if !ok {
		return Skill{}, false
	}
	if strings.TrimSpace(value.Key) == "" {
		value.Key = key
	}
	return value, true
}

// Materializer writes a skill source to a directory containing SKILL.md.
type Materializer interface {
	Materialize(ctx context.Context, s Skill) (sourcePath string, err error)
}

// Reserved Metadata keys interpreted by the SDK and drivers. Setting
// MetadataRuntimeName on a Skill overrides the directory name the
// materializer writes (and drivers mount); MetadataDisplayName is an
// admin-UI friendly alias.
const (
	MetadataRuntimeName = driver.SkillMetadataRuntimeName
	MetadataDisplayName = driver.SkillMetadataDisplayName
)

// Dir builds a Skill sourced from a local directory that contains
// SKILL.md (and optional reference files). The skill key defaults to
// the directory basename; callers may override it by assigning to the
// returned Skill's Key field.
func Dir(path string) Skill {
	return Skill{Key: keyFromPath(path), Source: PathSource{Path: path}}
}

// FS builds a Skill sourced from an io/fs.FS tree rooted at root. The
// root entry must contain SKILL.md; "" or "." mean the FS root. The
// skill key defaults to the basename of root ("skill" when root is
// empty); callers may override it by assigning to the returned Skill's
// Key field.
func FS(fsys fs.FS, root string) Skill {
	key := keyFromPath(root)
	if key == "" {
		key = "skill"
	}
	return Skill{Key: key, Source: FSSource{FS: fsys, Root: root}}
}

// Inline builds a Skill whose entire content is the given SKILL.md
// string. Key is required. Skills that need auxiliary reference files
// should use [FS] or [Archive] instead.
func Inline(key, skillMD string) Skill {
	return Skill{Key: key, Source: InlineSource{SkillMD: skillMD}}
}

// Key returns a Ref that references a provider-side skill by its
// catalogue key. The key is resolved by the [Provider] installed on
// the agent; unknown keys fail the run before the driver is invoked.
func Key(k string) Ref {
	return driver.SkillKey(k)
}

// Require returns a copy of s marked Required=true with the given
// human-readable reason. Required skills join the selected set of
// every run that sees them, regardless of what the caller passed to
// WithSkills.
func Require(s Skill, reason string) Skill {
	s.Required = true
	s.Reason = reason
	return s
}

func keyFromPath(value string) string {
	value = strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"), "/")
	if value == "" {
		return ""
	}
	base := path.Base(value)
	if base == "." || base == "/" {
		return ""
	}
	return base
}
