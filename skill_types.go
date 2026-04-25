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

// SkillSource is the open marker for a Skill's origin. Built-in
// implementations are SkillFromPath, SkillFromFS, SkillFromInline,
// SkillFromArchive (see archive_source.go). Hosts MAY define custom
// source types as long as a matching SkillMaterializer is installed
// via WithSkillMaterializer.
//
// SDK never branches on host-defined source types itself; it only
// routes them to the configured materializer. This keeps the SDK
// closed against host ontology while letting hosts own their fetch /
// unpack / cache strategy. See docs/v0.5.0-host-integration-plan.md
// §A1.2.4 for the rationale.
type SkillSource interface {
	// SkillSource is the marker method. It MUST be a no-op; its only
	// purpose is to constrain types that can be assigned to a Source
	// field. Custom types implement it as `func (T) SkillSource() {}`.
	SkillSource()
}

// SkillFromPath sources a skill from an existing local directory that
// contains SKILL.md (and optional references).
type SkillFromPath struct {
	Path string
}

// SkillSource implements [SkillSource].
func (SkillFromPath) SkillSource() {}

// SkillFromFS sources a skill from an io/fs.FS tree rooted at Root. The root
// entry must contain SKILL.md. Root == "" or "." is equivalent.
type SkillFromFS struct {
	FS   fs.FS
	Root string
}

// SkillSource implements [SkillSource].
func (SkillFromFS) SkillSource() {}

// SkillFromInline carries a single SKILL.md string. Callers that need
// auxiliary reference files should use SkillFromFS instead.
type SkillFromInline struct {
	SkillMD string
}

// SkillSource implements [SkillSource].
func (SkillFromInline) SkillSource() {}

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
