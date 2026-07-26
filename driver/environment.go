package driver

// ModelInfo is a model option visible through a driver. ID is the value
// accepted by config; Label is display text for UIs.
type ModelInfo struct {
	ID    string
	Label string
}

// DetectedModel reports the effective model inferred from config/profile
// state. Source names where the decision came from, such as explicit config or
// provider config file; Candidates records fallback values considered.
type DetectedModel struct {
	Model      string
	Provider   string
	Source     string
	Candidates []string
}

// AgentProfileSource identifies where a driver's effective local profile
// directory came from.
type AgentProfileSource string

const (
	// AgentProfileSourceBindingEnv means an explicit env binding selected the profile.
	AgentProfileSourceBindingEnv AgentProfileSource = "binding_env"
	// AgentProfileSourceProfileOption means a profile AgentOption selected the profile.
	AgentProfileSourceProfileOption AgentProfileSource = "profile_option"
	// AgentProfileSourceProcessEnv means a process environment variable selected the profile.
	AgentProfileSourceProcessEnv AgentProfileSource = "process_env"
	// AgentProfileSourceDefault means the driver used its native default path.
	AgentProfileSourceDefault AgentProfileSource = "default"
	// AgentProfileSourceManaged means the SDK/driver synthesized a managed profile.
	AgentProfileSourceManaged AgentProfileSource = "managed"
	// AgentProfileSourceUnsupported means the driver has no profile semantics.
	AgentProfileSourceUnsupported AgentProfileSource = "unsupported"
)

// AgentProfile reports the effective local operator profile directory for one
// bound driver as seen by the SDK control plane.
//
// Dir is the effective directory the built-in driver will inspect or use for
// local profile semantics. Source tells whether that directory came from an
// explicit CommonConfig.Env override, profile option, process
// environment fallback, a driver-native default path, or a driver-managed
// home. Managed is true only when the driver actively synthesizes an isolated
// profile directory, such as Codex's managed CODEX_HOME.
type AgentProfile struct {
	DriverType string
	Supported  bool
	Dir        string
	EnvVar     string
	Source     AgentProfileSource
	Managed    bool
	Error      string
}

// EnvironmentStatus summarizes the highest-severity environment check result.
type EnvironmentStatus string

const (
	// EnvironmentPass means all checks passed.
	EnvironmentPass EnvironmentStatus = "pass"
	// EnvironmentWarn means the driver may run but host attention is useful.
	EnvironmentWarn EnvironmentStatus = "warn"
	// EnvironmentFail means the driver is not ready to run.
	EnvironmentFail EnvironmentStatus = "fail"
)

// EnvironmentReport is the normalized admin-facing health report for one
// driver binding. Healthy remains for backward compatibility; new hosts should
// prefer Status plus the individual Checks.
type EnvironmentReport struct {
	DriverType string
	Status     EnvironmentStatus
	Healthy    bool
	Summary    string
	Checks     []EnvironmentCheck
}

// EnvironmentCheck is one probe result within an EnvironmentReport. Code is the
// stable machine-facing identifier; Message, Detail, and Hint are host-facing
// text that can be rendered directly in diagnostics UIs.
type EnvironmentCheck struct {
	Code    string
	Level   string
	Message string
	Detail  string
	Hint    string
}

// QuotaReport is the control-plane shape returned by Admin().GetQuota().
type QuotaReport struct {
	DriverType string
	Provider   string
	Source     string
	Available  bool
	Error      string
	Windows    []QuotaWindow
}

// QuotaWindow describes one quota/rate-limit/credit window reported by a
// driver-specific quota probe.
type QuotaWindow struct {
	Label       string
	UsedPercent *int
	ResetsAt    string
	ValueLabel  string
	Detail      string
}
