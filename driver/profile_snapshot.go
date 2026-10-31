package driver

import "context"

// ProfileResourceDriver is the optional complete profile-resource capability.
// SnapshotProfileResources MUST be read-only, including for a missing profile.
// It MUST distinguish desired resources from files actually observed. Sync
// materializes the declared resources and reports observed state; configuration,
// validation and I/O failures MUST be returned as errors. Undeclared families
// are left alone, while explicit empty declarations clear managed entries.
// Implementations MUST preserve external resources, serialize conflicting writes,
// and use the same captured Config and native resource semantics as Run.
// Snapshots MUST preserve DriverType, the effective profile and the desired
// payload fingerprint, and include each of the six ProfileResourceKind values
// exactly once. An absent profile has no observed Managed or External entries.
// A snapshot never claims a resource or runtime service was executed.
type ProfileResourceDriver interface {
	SnapshotProfileResources(ctx context.Context, cfg any, identity AgentIdentity, profile *ProfileSelection, payload ProfilePayload, selected []string, resolved []Skill) (ProfileSnapshot, error)
	SyncProfileResources(ctx context.Context, cfg any, identity AgentIdentity, profile *ProfileSelection, payload ProfilePayload, selected []string, resolved []Skill) (ProfileSnapshot, error)
}

// ProfileKind is the Driver-resolved effective-profile classification.
// It separates where the provider profile lives from the public
// ProfileSelection option that requested it.
type ProfileKind string

const (
	// ProfileKindShared means the effective profile is the provider-native
	// shared profile such as ~/.claude, ~/.codex, or ~/.cursor.
	ProfileKindShared ProfileKind = "shared"
	// ProfileKindHostManaged means the effective profile is isolated or
	// managed by the host/adapter, such as a dedicated, cloned, or managed home.
	ProfileKindHostManaged ProfileKind = "host_managed"
)

// ProfileResourceKind names one provider-visible resource family managed as
// part of the effective profile desired state.
type ProfileResourceKind string

const (
	ProfileResourceSkills       ProfileResourceKind = "skills"
	ProfileResourceMCP          ProfileResourceKind = "mcp"
	ProfileResourceAgents       ProfileResourceKind = "agents"
	ProfileResourceHooks        ProfileResourceKind = "hooks"
	ProfileResourceInstructions ProfileResourceKind = "instructions"
	ProfileResourceConfig       ProfileResourceKind = "config"
)

// ResourceSnapshot reports the observed state for one profile resource kind.
type ResourceSnapshot struct {
	Kind            ProfileResourceKind
	Fingerprint     string
	Managed         []string
	External        []string
	Support         ProfileResourceSupport
	Materialization ProfileResourceMaterialization
	Warnings        []string
	Error           string
}

// ProfileResourceSupport describes the supported vocabulary, not execution evidence.
type ProfileResourceSupport string

const (
	ProfileResourceSupportPortableCore     ProfileResourceSupport = "portable_core"
	ProfileResourceSupportPortableExtended ProfileResourceSupport = "portable_extended"
	ProfileResourceSupportNativeEscape     ProfileResourceSupport = "native_escape"
	ProfileResourceSupportFallback         ProfileResourceSupport = "fallback"
	ProfileResourceSupportUnsupported      ProfileResourceSupport = "unsupported"
)

// ProfileResourceMaterialization identifies the native or fallback delivery mechanism.
// Managed and External contain only entries actually observed through that mechanism.
type ProfileResourceMaterialization string

const (
	ProfileResourceMaterializationNativeManaged   ProfileResourceMaterialization = "native_managed"
	ProfileResourceMaterializationFileManaged     ProfileResourceMaterialization = "file_managed"
	ProfileResourceMaterializationPromptInjected  ProfileResourceMaterialization = "prompt_injected"
	ProfileResourceMaterializationFallback        ProfileResourceMaterialization = "fallback"
	ProfileResourceMaterializationNotMaterialized ProfileResourceMaterialization = "not_materialized"
)

// ProfileSnapshot reports the desired and observed state of an effective profile.
type ProfileSnapshot struct {
	DriverType  string
	Profile     AgentProfile
	Kind        ProfileKind
	Fingerprint string
	Resources   []ResourceSnapshot
	Warnings    []string
}
