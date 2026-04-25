package mockkit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type Config struct {
	Label string `json:"label"`
}

type RecordingDriver struct {
	Name          string
	RunFunc       func(context.Context, agentadaptor.DriverRunRequest, agentadaptor.EventSink) (agentadaptor.DriverRunResult, error)
	Schema        *agentadaptor.ConfigSchema
	Quota         *agentadaptor.QuotaReport
	DetectedModel *agentadaptor.DetectedModel
	Profile       *agentadaptor.AgentProfile
	mu            sync.Mutex
	lastReq       agentadaptor.DriverRunRequest
	runCount      int
}

func NewRecordingDriver(name string) *RecordingDriver {
	return &RecordingDriver{Name: name}
}

func (d *RecordingDriver) Descriptor() agentadaptor.DriverDescriptor {
	display := d.Name
	if display == "" {
		display = "Mock Driver"
	}
	return agentadaptor.DriverDescriptor{
		Type:        "mock",
		DisplayName: display,
		ConfigSchema: &agentadaptor.ConfigSchema{
			Fields: []agentadaptor.ConfigField{
				{Name: "label", Type: "text", Description: "Human-readable label used by the example mock adapter."},
			},
		},
		Sessions: agentadaptor.SessionCapability{SupportsResume: true},
		Skills:   agentadaptor.SkillCapability{Supported: true, Mode: agentadaptor.SkillSyncEphemeral},
		Runtime:  agentadaptor.RuntimeCapability{ReportsServices: true},
		Instructions: agentadaptor.InstructionsCapability{
			Supported: true,
		},
		Workspace: agentadaptor.WorkspaceCapability{
			Supported: true,
		},
	}
}

func (d *RecordingDriver) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case Config, *Config:
		return nil
	default:
		return fmt.Errorf("mock driver requires mockkit.Config, got %T", cfg)
	}
}

func (d *RecordingDriver) CheckEnvironment(_ context.Context, _ any) (agentadaptor.EnvironmentReport, error) {
	return agentadaptor.EnvironmentReport{
		DriverType: "mock",
		Status:     agentadaptor.EnvironmentPass,
		Healthy:    true,
		Summary:    "mock environment checks passed",
		Checks: []agentadaptor.EnvironmentCheck{
			{Code: "mock_ready", Level: "info", Message: "mock driver is ready"},
		},
	}, nil
}

func (d *RecordingDriver) ListModels(_ context.Context, _ any) ([]agentadaptor.ModelInfo, error) {
	return []agentadaptor.ModelInfo{
		{ID: "mock-model", Label: "mock-model"},
	}, nil
}

func (d *RecordingDriver) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	d.mu.Lock()
	d.runCount++
	d.lastReq = cloneRunRequest(req)
	d.mu.Unlock()

	if d.RunFunc != nil {
		return d.RunFunc(ctx, req, sink)
	}

	raw, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	cfg := readConfig(req.Config)
	return agentadaptor.DriverRunResult{
		Output:          "mock recording completed",
		RawStreams:      &agentadaptor.RawStreams{Stdout: string(raw)},
		ExitCode:        0,
		Model:           cfg.Label,
		Summary:         "mock recording completed",
		RuntimeServices: cloneRuntimeServiceReports(runtimeReportsFromRefs(req.Runtime.Ensured, req.Agent)),
	}, nil
}

func (d *RecordingDriver) LastRequest() agentadaptor.DriverRunRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return cloneRunRequest(d.lastReq)
}

func (d *RecordingDriver) RunCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runCount
}

func (d *RecordingDriver) DetectModel(_ context.Context, _ any) (*agentadaptor.DetectedModel, error) {
	if d.DetectedModel == nil {
		return nil, nil
	}
	copyModel := *d.DetectedModel
	copyModel.Candidates = append([]string(nil), d.DetectedModel.Candidates...)
	return &copyModel, nil
}

func (d *RecordingDriver) GetProfile(_ context.Context, _ any, _ agentadaptor.AgentIdentity, _ *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error) {
	if d.Profile == nil {
		return agentadaptor.AgentProfile{
			DriverType: "mock",
			Supported:  false,
			Source:     agentadaptor.AgentProfileSourceUnsupported,
			Error:      "mock profile not configured",
		}, nil
	}
	profile := *d.Profile
	return profile, nil
}

func (d *RecordingDriver) ConfigSchema(_ context.Context, _ any) (*agentadaptor.ConfigSchema, error) {
	if d.Schema != nil {
		copySchema := *d.Schema
		copySchema.Fields = append([]agentadaptor.ConfigField(nil), d.Schema.Fields...)
		return &copySchema, nil
	}
	return d.Descriptor().ConfigSchema, nil
}

func (d *RecordingDriver) GetQuota(_ context.Context, _ any) (agentadaptor.QuotaReport, error) {
	if d.Quota == nil {
		return agentadaptor.QuotaReport{DriverType: "mock", Available: false}, nil
	}
	report := *d.Quota
	report.Windows = append([]agentadaptor.QuotaWindow(nil), d.Quota.Windows...)
	return report, nil
}

type ObservingWorkspaceManager struct{}

func (ObservingWorkspaceManager) Resolve(_ context.Context, req agentadaptor.WorkspaceRequest) (agentadaptor.WorkspaceLease, error) {
	cwd := req.BaseCWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return agentadaptor.WorkspaceLease{}, err
		}
	}

	mode := agentadaptor.WorkspaceModeShared
	strategy := agentadaptor.WorkspaceStrategyProjectPrimary
	metadata := cloneStringMap(req.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["workspace_manager"] = "mockkit"

	switch spec := req.Spec.(type) {
	case nil:
	case agentadaptor.GitWorktreeWorkspace:
		mode = agentadaptor.WorkspaceModeIsolated
		strategy = agentadaptor.WorkspaceStrategyGitWorktree
		metadata["base_ref"] = spec.BaseRef
		metadata["branch_template"] = spec.BranchTemplate
	case agentadaptor.SharedWorkspace:
		mode = agentadaptor.WorkspaceModeShared
		strategy = agentadaptor.WorkspaceStrategyProjectPrimary
	case agentadaptor.AdapterManagedWorkspace:
		mode = agentadaptor.WorkspaceModeAgentDefault
		strategy = agentadaptor.WorkspaceStrategyAdapterManaged
	default:
		return agentadaptor.WorkspaceLease{}, fmt.Errorf("unsupported workspace spec type %T", req.Spec)
	}

	fingerprintPayload := map[string]any{
		"cwd":      cwd,
		"mode":     mode,
		"strategy": strategy,
		"metadata": metadata,
	}
	return agentadaptor.WorkspaceLease{
		ID:           stableDigest("workspace-id", fingerprintPayload),
		Mode:         mode,
		StrategyType: strategy,
		CWD:          cwd,
		Fingerprint:  stableDigest("workspace-fingerprint", fingerprintPayload),
		Metadata:     metadata,
	}, nil
}

func (ObservingWorkspaceManager) Release(_ context.Context, _ agentadaptor.WorkspaceLease, _ agentadaptor.WorkspaceReleaseMode) error {
	return nil
}

// NewSkillSet builds an agentadaptor.SkillSet seeded with inline skills that
// have their runtime name defaulted to the skill key. It is the new replacement
// for the legacy StaticSkillCatalog/StaticSkillAssembler pair.
func NewSkillSet(entries map[string]agentadaptor.Skill) agentadaptor.SkillSet {
	out := agentadaptor.SkillSet{}
	for mapKey, skill := range entries {
		s := cloneSkill(skill)
		if s.Key == "" {
			s.Key = mapKey
		}
		if s.Source == nil {
			s.Source = agentadaptor.SkillFromInline{SkillMD: "# " + s.Key}
		}
		out[s.Key] = s
	}
	return out
}

// InlineSkill returns a convenience Skill backed by an inline SKILL.md string
// with a deterministic _runtime_name metadata entry (equal to the key).
func InlineSkill(key, body string) agentadaptor.Skill {
	return agentadaptor.Skill{
		Key:      key,
		Source:   agentadaptor.SkillFromInline{SkillMD: body},
		Metadata: map[string]string{agentadaptor.SkillMetadataRuntimeName: key},
	}
}

type ObservingRuntimeManager struct {
	Refs           []agentadaptor.RuntimeServiceRef
	LastRequest    agentadaptor.RuntimeServiceRequest
	ReleasedRuns   []string
	ReleasedLabels []map[string]string
}

func (m *ObservingRuntimeManager) Ensure(_ context.Context, req agentadaptor.RuntimeServiceRequest) ([]agentadaptor.RuntimeServiceRef, error) {
	m.LastRequest = req
	return cloneRuntimeServiceRefs(m.Refs), nil
}

func (m *ObservingRuntimeManager) ReleaseByRun(_ context.Context, runID string) error {
	m.ReleasedRuns = append(m.ReleasedRuns, runID)
	return nil
}

// ReleaseByLabels records the call so tests can assert which label
// sets the host issued. Empty maps are recorded verbatim so tests
// can verify the "noop on empty" guard at the SDK boundary.
func (m *ObservingRuntimeManager) ReleaseByLabels(_ context.Context, labels map[string]string) error {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	m.ReleasedLabels = append(m.ReleasedLabels, out)
	return nil
}

func cloneRunRequest(req agentadaptor.DriverRunRequest) agentadaptor.DriverRunRequest {
	out := req
	out.RunID = req.RunID
	out.Metadata = cloneStringMap(req.Metadata)
	out.Workspace = req.Workspace
	out.Workspace.Metadata = cloneStringMap(req.Workspace.Metadata)
	out.Runtime = cloneRuntimePayload(req.Runtime)
	out.Skills = cloneResolvedSkills(req.Skills)
	out.Instructions = cloneInstructions(req.Instructions)
	out.Session = cloneSessionContext(req.Session)
	return out
}

func cloneResolvedSkills(payload agentadaptor.ResolvedSkills) agentadaptor.ResolvedSkills {
	out := agentadaptor.ResolvedSkills{
		Mode:        payload.Mode,
		Fingerprint: payload.Fingerprint,
	}
	if len(payload.Entries) > 0 {
		out.Entries = make([]agentadaptor.ResolvedSkill, 0, len(payload.Entries))
		for _, entry := range payload.Entries {
			out.Entries = append(out.Entries, agentadaptor.ResolvedSkill{
				Key:         entry.Key,
				RuntimeName: entry.RuntimeName,
				SourcePath:  entry.SourcePath,
				Required:    entry.Required,
				Reason:      entry.Reason,
				Metadata:    cloneStringMap(entry.Metadata),
			})
		}
	}
	if len(payload.Warnings) > 0 {
		out.Warnings = append([]string(nil), payload.Warnings...)
	}
	return out
}

func cloneSkill(skill agentadaptor.Skill) agentadaptor.Skill {
	return agentadaptor.Skill{
		Key:      skill.Key,
		Source:   skill.Source,
		Required: skill.Required,
		Reason:   skill.Reason,
		Metadata: cloneStringMap(skill.Metadata),
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneInstructions(ref *agentadaptor.InstructionsBundleRef) *agentadaptor.InstructionsBundleRef {
	if ref == nil {
		return nil
	}
	copyRef := *ref
	return &copyRef
}

func cloneSessionContext(session *agentadaptor.DriverSessionContext) *agentadaptor.DriverSessionContext {
	if session == nil {
		return nil
	}
	out := *session
	if session.State != nil {
		stateCopy := *session.State
		stateCopy.Data = cloneStringMap(session.State.Data)
		out.State = &stateCopy
	}
	return &out
}

func readConfig(cfg any) Config {
	switch typed := cfg.(type) {
	case Config:
		return typed
	case *Config:
		if typed != nil {
			return *typed
		}
	}
	return Config{}
}

func cloneRuntimePayload(payload agentadaptor.RuntimePayload) agentadaptor.RuntimePayload {
	return agentadaptor.RuntimePayload{
		Requested:   cloneRuntimeServiceSpecs(payload.Requested),
		Ensured:     cloneRuntimeServiceRefs(payload.Ensured),
		Fingerprint: payload.Fingerprint,
	}
}

func cloneRuntimeServiceSpecs(values []agentadaptor.RuntimeServiceSpec) []agentadaptor.RuntimeServiceSpec {
	if len(values) == 0 {
		return nil
	}
	out := make([]agentadaptor.RuntimeServiceSpec, 0, len(values))
	for _, value := range values {
		out = append(out, agentadaptor.RuntimeServiceSpec{
			ID:          value.ID,
			Name:        value.Name,
			URL:         value.URL,
			Description: value.Description,
			Lifecycle:   value.Lifecycle,
			ReuseKey:    value.ReuseKey,
			Command:     value.Command,
			CWD:         value.CWD,
			Port:        value.Port,
			Metadata:    cloneStringMap(value.Metadata),
		})
	}
	return out
}

func cloneRuntimeServiceRefs(values []agentadaptor.RuntimeServiceRef) []agentadaptor.RuntimeServiceRef {
	if len(values) == 0 {
		return nil
	}
	out := make([]agentadaptor.RuntimeServiceRef, 0, len(values))
	for _, value := range values {
		out = append(out, agentadaptor.RuntimeServiceRef{
			ID:           value.ID,
			Name:         value.Name,
			URL:          value.URL,
			Status:       value.Status,
			Lifecycle:    value.Lifecycle,
			ReuseKey:     value.ReuseKey,
			Command:      value.Command,
			CWD:          value.CWD,
			Port:         value.Port,
			OwnerAgentID: value.OwnerAgentID,
			Health:       value.Health,
			Metadata:     cloneStringMap(value.Metadata),
		})
	}
	return out
}

func runtimeReportsFromRefs(refs []agentadaptor.RuntimeServiceRef, owner agentadaptor.AgentIdentity) []agentadaptor.RuntimeServiceReport {
	if len(refs) == 0 {
		return nil
	}
	out := make([]agentadaptor.RuntimeServiceReport, 0, len(refs))
	for _, ref := range refs {
		status := ref.Status
		if status == "" {
			status = agentadaptor.RuntimeServiceRunning
		}
		lifecycle := ref.Lifecycle
		if lifecycle == "" {
			lifecycle = agentadaptor.RuntimeLifecycleShared
		}
		ownerID := ref.OwnerAgentID
		if ownerID == "" {
			ownerID = owner.ID
		}
		health := ref.Health
		if health == "" {
			health = agentadaptor.RuntimeHealthUnknown
		}
		out = append(out, agentadaptor.RuntimeServiceReport{
			ID:           ref.ID,
			Name:         ref.Name,
			URL:          ref.URL,
			Status:       status,
			Lifecycle:    lifecycle,
			ReuseKey:     ref.ReuseKey,
			Command:      ref.Command,
			CWD:          ref.CWD,
			Port:         ref.Port,
			OwnerAgentID: ownerID,
			Health:       health,
			Metadata:     cloneStringMap(ref.Metadata),
		})
	}
	return out
}

func cloneRuntimeServiceReports(values []agentadaptor.RuntimeServiceReport) []agentadaptor.RuntimeServiceReport {
	if len(values) == 0 {
		return nil
	}
	out := make([]agentadaptor.RuntimeServiceReport, 0, len(values))
	for _, value := range values {
		out = append(out, agentadaptor.RuntimeServiceReport{
			ID:           value.ID,
			Name:         value.Name,
			URL:          value.URL,
			Status:       value.Status,
			Lifecycle:    value.Lifecycle,
			ReuseKey:     value.ReuseKey,
			Command:      value.Command,
			CWD:          value.CWD,
			Port:         value.Port,
			OwnerAgentID: value.OwnerAgentID,
			Health:       value.Health,
			Metadata:     cloneStringMap(value.Metadata),
		})
	}
	return out
}

func stableDigest(prefix string, value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return prefix
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%s-%x", prefix, sum[:8])
}
