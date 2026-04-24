package agentadaptor

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Skill is the canonical description of one skill: who it is (Key), where it
// comes from (Source), and whether it must participate in every run that sees
// it (Required). See docs/skill-api-design.md §5.1 for the full contract.
//
// Skill also acts as a SkillRef so callers can pass a Skill value directly to
// WithDefaultSkills / WithSkills without first registering it in a provider.
type Skill struct {
	// Key is the business-facing identifier of the skill. It is compared
	// case-sensitively during merging; any two Skill values that share a Key
	// must be structurally equal (see ErrSkillKeyConflict).
	Key string
	// Source describes how the SDK should locate / materialize the SKILL.md
	// content. Source == nil is invalid; the SDK reports ErrSkillSourceMissing
	// at construction time (for binding-level values) or at Run() time (for
	// per-run values).
	Source SkillSource
	// Required marks the skill as must-install. Required skills are added to
	// the Selected set for every run regardless of what the caller passed in
	// WithSkills.
	Required bool
	// Reason is a human-readable explanation attached to Required skills.
	// Rendered by Admin UIs; ignored when Required is false.
	Reason string
	// Metadata carries optional extension fields. Keys with an underscore
	// prefix are reserved for SDK-level interpretation (see the reserved
	// keys documented in docs/skill-api-design.md §5.1).
	Metadata map[string]string
}

// Reserved Metadata keys interpreted by the SDK / adapters.
const (
	// SkillMetadataRuntimeName overrides the directory name used when the
	// materializer writes the skill to disk (and when adapters such as
	// Cursor mount it under <home>/skills/<name>). Defaults to slug(Key).
	SkillMetadataRuntimeName = "_runtime_name"
	// SkillMetadataDisplayName is an Admin UI friendly alias.
	SkillMetadataDisplayName = "_display_name"
)

// isSkillRef is the marker that makes Skill a SkillRef value.
func (Skill) isSkillRef() {}

// SkillSource is the sum type describing a Skill's origin. Implementations
// are defined in this file: SkillFromPath, SkillFromFS, SkillFromInline.
type SkillSource interface {
	isSkillSource()
}

// SkillFromPath sources a skill from an existing local directory that
// contains SKILL.md (and optional references).
type SkillFromPath struct {
	Path string
}

func (SkillFromPath) isSkillSource() {}

// SkillFromFS sources a skill from an io/fs.FS tree rooted at Root. The root
// entry must contain SKILL.md. Root == "" or "." is equivalent.
type SkillFromFS struct {
	FS   fs.FS
	Root string
}

func (SkillFromFS) isSkillSource() {}

// SkillFromInline carries a single SKILL.md string. Callers that need
// auxiliary reference files should use SkillFromFS instead.
type SkillFromInline struct {
	SkillMD string
}

func (SkillFromInline) isSkillSource() {}

// SkillRef is accepted by WithDefaultSkills and WithSkills. It is either
// a string key (SkillKey) or a fully-defined Skill value.
type SkillRef interface {
	isSkillRef()
}

// SkillKey wraps a plain skill key string for use as a SkillRef.
type SkillKey string

func (SkillKey) isSkillRef() {}

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

// SkillProvider is the single host-facing skill hook. Implementations return
// the full catalogue of skills visible to tenantID, including any entries
// the provider itself marks Required=true.
//
// Providers that genuinely cannot enumerate their catalogue must return
// ErrSkillsNotEnumerable. The SDK then reports the Admin surface as
// unsupported and skips provider-driven injection for runs.
type SkillProvider interface {
	List(ctx context.Context, tenantID string) ([]Skill, error)
}

// SkillSet is a static map-based SkillProvider convenient for hosts whose
// skill catalogue is known at construction time.
type SkillSet map[string]Skill

// List satisfies SkillProvider.
func (s SkillSet) List(_ context.Context, _ string) ([]Skill, error) {
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

// SkillMaterializer lets hosts customise how SkillFromFS / SkillFromInline
// sources are written to disk before an adapter consumes them. The returned
// sourcePath must be a directory containing SKILL.md.
//
// Implementations are responsible for caching, atomic writes, and any
// host-specific isolation (for example multi-tenant cache roots). The SDK
// default implementation is documented in docs/skill-api-design.md §5.7.
type SkillMaterializer interface {
	Materialize(ctx context.Context, s Skill) (sourcePath string, err error)
}

// ResolvedSkills is the adapter-facing view of a run's Selected skills. It
// is produced internally by the SDK and is not intended for host
// construction.
//
// Contract between SDK and adapter:
//
//   - Entries is the subset of the selected set that successfully
//     materialized. Entries are listed in the order they were selected and
//     the order is stable across runs with equivalent inputs.
//   - For the ListSkills / SyncSkills paths, the SDK additionally passes a
//     parallel selected []string whose contents are exactly ResolvedSkills.
//     Keys(). Adapters MAY rely on that equivalence; hosts MUST NOT observe
//     divergence through the ResolvedSkills value alone.
//   - Warnings carries non-fatal messages (for example "skill X failed to
//     materialize, dropped from Entries"). Adapters should forward these
//     to the caller via SkillSnapshot.Warnings.
//   - Fingerprint is a deterministic digest of Entries and Warnings. Two
//     runs whose ResolvedSkills produce the same Fingerprint are guaranteed
//     to have identical skill-visible state.
type ResolvedSkills struct {
	Mode        SkillSyncMode
	Entries     []ResolvedSkill
	Warnings    []string
	Fingerprint string
}

// ResolvedSkill carries the post-materialization information an adapter needs
// to install or expose a single skill for the current run.
type ResolvedSkill struct {
	Key         string
	RuntimeName string
	SourcePath  string
	Required    bool
	Reason      string
	Metadata    map[string]string
}

// Keys returns the list of ResolvedSkill keys in their current order.
func (r ResolvedSkills) Keys() []string {
	out := make([]string, 0, len(r.Entries))
	for _, entry := range r.Entries {
		out = append(out, entry.Key)
	}
	return out
}

// SkillSyncMode describes how an adapter surfaces skills for one run.
type SkillSyncMode string

const (
	SkillSyncUnsupported SkillSyncMode = "unsupported"
	SkillSyncEphemeral   SkillSyncMode = "ephemeral"
	SkillSyncPersistent  SkillSyncMode = "persistent"
)

// SkillSnapshot is the Admin-layer report for ListSkills / SetSelectedSkills.
// See docs/skill-api-design.md §5.8.
type SkillSnapshot struct {
	DriverType  string
	Supported   bool
	Mode        SkillSyncMode
	Selected    []string
	Resolved    []Skill
	Entries     []SnapshotEntry
	Warnings    []string
	Fingerprint string
}

// SnapshotEntry is one Admin-layer skill status entry. It replaces the old
// SkillEntry type and uses consistent terminology (Selected instead of
// Desired) across the public surface.
type SnapshotEntry struct {
	Key            string
	RuntimeName    string
	Selected       bool
	Managed        bool
	Required       bool
	RequiredReason string
	State          SkillState
	Origin         SkillOrigin
	OriginLabel    string
	LocationLabel  string
	ReadOnly       bool
	SourcePath     string
	TargetPath     string
	Detail         string
}

// SkillState / SkillOrigin describe adapter-layer classification. The values
// are unchanged from the previous API.
type SkillState string
type SkillOrigin string

const (
	SkillStateAvailable  SkillState = "available"
	SkillStateConfigured SkillState = "configured"
	SkillStateInstalled  SkillState = "installed"
	SkillStateMissing    SkillState = "missing"
	SkillStateStale      SkillState = "stale"
	SkillStateExternal   SkillState = "external"
)

const (
	SkillOriginManaged  SkillOrigin = "company_managed"
	SkillOriginRequired SkillOrigin = "paperclip_required"
	SkillOriginUser     SkillOrigin = "user_installed"
	SkillOriginUnknown  SkillOrigin = "external_unknown"
)

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
