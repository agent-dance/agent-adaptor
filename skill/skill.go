package skill

import (
	"io/fs"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// Core vocabulary aliases. Each name points at the identical public
// declaration in the root package (which in turn aliases the SPI /
// engine truth), so a value built here is byte-for-byte the same type
// hosts and drivers already exchange.
type (
	// Skill is the full description of one skill: identity (Key),
	// origin (Source), Required marker, human-readable Reason, and
	// optional Metadata. Skill values act as [Ref], so constructor
	// results can be passed straight to WithSkills /
	// WithDefaultSkills.
	Skill = agentadaptor.Skill

	// Ref is what WithSkills / WithDefaultSkills accept: either a
	// provider catalogue key (built with [Key]) or a fully-defined
	// [Skill] value (built with [Dir], [FS], [Inline], or [Archive]).
	Ref = agentadaptor.SkillRef

	// Source is the open marker interface for a Skill's origin.
	// Hosts may define custom Source types as long as a matching
	// [Materializer] is installed to handle them.
	Source = agentadaptor.SkillSource

	// Provider is the host extension point that resolves catalogue
	// keys (see [Key]) into concrete Skill definitions on every run.
	// Implementations may also inject tenant-mandatory Required
	// skills the caller did not reference.
	Provider = agentadaptor.SkillProvider

	// Catalog extends [Provider] with full-catalogue enumeration for
	// admin surfaces. Providers whose catalogue is too large to
	// enumerate implement only Provider.
	Catalog = agentadaptor.SkillCatalog

	// Set is a static map-based [Catalog], convenient when the whole
	// catalogue is known at construction time.
	Set = agentadaptor.SkillSet

	// Materializer is the host extension point that controls how a
	// Skill's Source is written to a local SKILL.md directory before
	// a driver consumes it.
	Materializer = agentadaptor.SkillMaterializer
)

// Reserved Metadata keys interpreted by the SDK and drivers. Setting
// MetadataRuntimeName on a Skill overrides the directory name the
// materializer writes (and drivers mount); MetadataDisplayName is an
// admin-UI friendly alias.
const (
	MetadataRuntimeName = agentadaptor.SkillMetadataRuntimeName
	MetadataDisplayName = agentadaptor.SkillMetadataDisplayName
)

// Dir builds a Skill sourced from a local directory that contains
// SKILL.md (and optional reference files). The skill key defaults to
// the directory basename; callers may override it by assigning to the
// returned Skill's Key field.
func Dir(path string) Skill {
	return agentadaptor.LocalSkill(path)
}

// FS builds a Skill sourced from an io/fs.FS tree rooted at root. The
// root entry must contain SKILL.md; "" or "." mean the FS root. The
// skill key defaults to the basename of root ("skill" when root is
// empty); callers may override it by assigning to the returned Skill's
// Key field.
func FS(fsys fs.FS, root string) Skill {
	return agentadaptor.FSSkill(fsys, root)
}

// Inline builds a Skill whose entire content is the given SKILL.md
// string. Key is required. Skills that need auxiliary reference files
// should use [FS] or [Archive] instead.
func Inline(key, skillMD string) Skill {
	return agentadaptor.InlineSkill(key, skillMD)
}

// Key returns a Ref that references a provider-side skill by its
// catalogue key. The key is resolved by the [Provider] installed on
// the agent; unknown keys fail the run before the driver is invoked.
func Key(k string) Ref {
	return agentadaptor.Key(k)
}

// Require returns a copy of s marked Required=true with the given
// human-readable reason. Required skills join the selected set of
// every run that sees them, regardless of what the caller passed to
// WithSkills.
func Require(s Skill, reason string) Skill {
	return agentadaptor.Require(s, reason)
}
