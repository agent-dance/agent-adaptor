package driver

// Skill is the canonical description of one skill: who it is (Key), where it
// comes from (Source), and whether it must participate in every run that sees
// it (Required). See docs/skill-api-design.md §1 for the full contract.
//
// Skill also acts as a SkillRef so callers can pass a Skill value directly to
// adaptor.WithSkills without first registering it in a provider.
type Skill struct {
	// Key is the business-facing identifier of the skill. It is compared
	// case-sensitively during merging; any two Skill values that share a Key
	// must be structurally equal (see ErrSkillKeyConflict).
	Key string
	// Source describes how the SDK should locate / materialize the SKILL.md
	// content. Source == nil is invalid; the SDK reports ErrSkillSourceMissing
	// while resolving Agent defaults or a Run/Stream invocation.
	Source SkillSource
	// Required marks the skill as must-install. Required skills are added to
	// the Selected set for every run regardless of what the caller passed in
	// WithSkills.
	Required bool
	// Reason is a human-readable explanation attached to Required skills.
	// Rendered by host UIs; ignored when Required is false.
	Reason string
	// Metadata carries optional extension fields. Keys with an underscore
	// prefix are reserved for SDK-level interpretation (see the reserved
	// keys documented in docs/skill-api-design.md §1).
	Metadata map[string]string
}

// Reserved Metadata keys interpreted by the SDK / drivers.
const (
	// SkillMetadataRuntimeName overrides the directory name used when the
	// materializer writes the skill to disk (and when drivers such as
	// Cursor mount it under <home>/skills/<name>). Defaults to slug(Key).
	SkillMetadataRuntimeName = "_runtime_name"
	// SkillMetadataDisplayName is a host-UI-friendly label.
	SkillMetadataDisplayName = "_display_name"
)

// isSkillRef is the marker that makes Skill a SkillRef value.
func (Skill) isSkillRef() {}

// SkillSource is the open marker for a Skill's origin. Built-in sources and
// constructors live in package skill. Hosts MAY define custom source types as
// long as a matching skill.Materializer is installed with
// adaptor.WithSkillMaterializer.
//
// SDK never branches on host-defined source types itself; it only
// routes them to the configured materializer. This keeps the SDK
// closed against host ontology while letting hosts own their fetch /
// unpack / cache strategy. See docs/skill-api-design.md §3 for the
// materializer contract.
type SkillSource interface {
	// SkillSource is the marker method. It MUST be a no-op; its only
	// purpose is to constrain types that can be assigned to a Source
	// field. Custom types implement it as `func (T) SkillSource() {}`.
	SkillSource()
}

// SkillRef is accepted by adaptor.WithSkills. It is either a catalogue key
// (SkillKey) or a fully-defined Skill value.
type SkillRef interface {
	isSkillRef()
}

// SkillKey wraps a plain skill key string for use as a SkillRef.
type SkillKey string

func (SkillKey) isSkillRef() {}

// ResolvedSkills is the driver-facing view of a run's Selected skills. It
// is produced internally by the SDK and is not intended for host
// construction.
//
// Contract between SDK and driver:
//
//   - Entries contains every selected skill after successful materialization.
//     A selected skill that cannot be materialized fails resolution with
//     ErrSkillMaterializationFailed before the driver is invoked.
//   - For the ListSkills / SyncSkills paths, the SDK additionally passes a
//     parallel selected []string whose contents are exactly ResolvedSkills.
//     Keys(). Drivers MAY rely on that equivalence; hosts MUST NOT observe
//     divergence through the ResolvedSkills value alone.
//   - Warnings carries non-fatal messages. Materialization failures are fatal
//     and are not represented as warnings.
//   - Fingerprint is a deterministic digest of Entries and Warnings. Two
//     runs whose ResolvedSkills produce the same Fingerprint are guaranteed
//     to have identical skill-visible state.
type ResolvedSkills struct {
	Mode        SkillSyncMode
	Entries     []ResolvedSkill
	Warnings    []string
	Fingerprint string
}

// ResolvedSkill carries the post-materialization information a driver needs
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

// SkillSyncMode describes how a driver surfaces skills for one run.
type SkillSyncMode string

const (
	// SkillSyncUnsupported means the driver ignores SDK-resolved skills or
	// cannot report observed skill state through Agent.Inspect().Skills.
	SkillSyncUnsupported SkillSyncMode = "unsupported"
	// SkillSyncEphemeral means skills are materialized for the current run or
	// managed profile and do not represent durable user configuration.
	SkillSyncEphemeral SkillSyncMode = "ephemeral"
	// SkillSyncPersistent means the driver exposes or updates a durable
	// provider-side skill installation.
	SkillSyncPersistent SkillSyncMode = "persistent"
)

// SkillSnapshot is the inspection and synchronization report returned through
// Agent.Inspect().Skills, Agent.SelectSkills, and Agent.SyncProfile.
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

// SnapshotEntry is one observed or desired skill status entry in a
// SkillSnapshot.
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

// SkillState describes driver-layer status for one skill snapshot entry.
type SkillState string

// SkillOrigin describes who owns or installed one skill snapshot entry.
type SkillOrigin string

const (
	// SkillStateAvailable means the skill is known to the catalogue but not selected.
	SkillStateAvailable SkillState = "available"
	// SkillStateConfigured means the driver/profile has the skill configured.
	SkillStateConfigured SkillState = "configured"
	// SkillStateInstalled means the required files are present in the driver runtime.
	SkillStateInstalled SkillState = "installed"
	// SkillStateMissing means a selected skill could not be found where expected.
	SkillStateMissing SkillState = "missing"
	// SkillStateStale means a persistent skill exists but differs from the SDK input.
	SkillStateStale SkillState = "stale"
	// SkillStateExternal means the driver found a skill outside SDK management.
	SkillStateExternal SkillState = "external"
)

const (
	// SkillOriginManaged marks SDK/host-managed skills.
	SkillOriginManaged SkillOrigin = "company_managed"
	// SkillOriginRequired marks skills selected because the provider declared
	// them Required.
	SkillOriginRequired SkillOrigin = "paperclip_required"
	// SkillOriginUser marks skills installed by the operator/user.
	SkillOriginUser SkillOrigin = "user_installed"
	// SkillOriginUnknown marks externally discovered skills whose owner is unknown.
	SkillOriginUnknown SkillOrigin = "external_unknown"
)
