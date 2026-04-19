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
		Output:          string(raw),
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

func (d *RecordingDriver) GetProfile(_ context.Context, _ any, _ agentadaptor.AgentIdentity) (agentadaptor.AgentProfile, error) {
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

type StaticSkillCatalog struct {
	Entries map[string]agentadaptor.Skill
}

func (c StaticSkillCatalog) Resolve(_ context.Context, _ string, refs []string) ([]agentadaptor.Skill, error) {
	out := make([]agentadaptor.Skill, 0, len(refs))
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		if skill, ok := c.Entries[ref]; ok {
			out = append(out, cloneSkill(skill))
			continue
		}
		out = append(out, agentadaptor.Skill{
			Key:     ref,
			Runtime: ref,
			Content: "# " + ref,
		})
	}
	return out, nil
}

type StaticSkillAssembler struct {
	Mode          agentadaptor.SkillSyncMode
	RuntimePrefix string
}

type ObservingRuntimeManager struct {
	Refs         []agentadaptor.RuntimeServiceRef
	LastRequest  agentadaptor.RuntimeServiceRequest
	ReleasedRuns []string
}

func (m *ObservingRuntimeManager) Ensure(_ context.Context, req agentadaptor.RuntimeServiceRequest) ([]agentadaptor.RuntimeServiceRef, error) {
	m.LastRequest = req
	return cloneRuntimeServiceRefs(m.Refs), nil
}

func (m *ObservingRuntimeManager) ReleaseByRun(_ context.Context, runID string) error {
	m.ReleasedRuns = append(m.ReleasedRuns, runID)
	return nil
}

func (a StaticSkillAssembler) Prepare(_ context.Context, req agentadaptor.SkillAssemblyRequest) (agentadaptor.SkillPayload, error) {
	resolved := make([]agentadaptor.Skill, 0, len(req.Resolved))
	for _, skill := range req.Resolved {
		copySkill := cloneSkill(skill)
		if a.RuntimePrefix != "" {
			copySkill.Runtime = a.RuntimePrefix + copySkill.Key
		} else if copySkill.Runtime == "" {
			copySkill.Runtime = copySkill.Key
		}
		resolved = append(resolved, copySkill)
	}

	mode := a.Mode
	if mode == "" {
		mode = agentadaptor.SkillSyncEphemeral
	}

	fingerprintPayload := map[string]any{
		"driver_type": req.DriverType,
		"tenant_id":   req.TenantID,
		"requested":   append([]string(nil), req.Requested...),
		"resolved":    resolved,
		"mode":        mode,
	}
	return agentadaptor.SkillPayload{
		Mode:        mode,
		Requested:   append([]string(nil), req.Requested...),
		Resolved:    resolved,
		Fingerprint: stableDigest("skills", fingerprintPayload),
	}, nil
}

func cloneRunRequest(req agentadaptor.DriverRunRequest) agentadaptor.DriverRunRequest {
	out := req
	out.RunID = req.RunID
	out.Metadata = cloneStringMap(req.Metadata)
	out.Workspace = req.Workspace
	out.Workspace.Metadata = cloneStringMap(req.Workspace.Metadata)
	out.Runtime = cloneRuntimePayload(req.Runtime)
	out.Skills = cloneSkillPayload(req.Skills)
	out.Instructions = cloneInstructions(req.Instructions)
	out.Session = cloneSessionContext(req.Session)
	return out
}

func cloneSkillPayload(payload agentadaptor.SkillPayload) agentadaptor.SkillPayload {
	return agentadaptor.SkillPayload{
		Mode:           payload.Mode,
		Requested:      append([]string(nil), payload.Requested...),
		Resolved:       cloneSkills(payload.Resolved),
		RuntimeEntries: append([]agentadaptor.SkillRuntimeEntry(nil), payload.RuntimeEntries...),
		Warnings:       append([]string(nil), payload.Warnings...),
		Fingerprint:    payload.Fingerprint,
	}
}

func cloneSkills(skills []agentadaptor.Skill) []agentadaptor.Skill {
	if len(skills) == 0 {
		return nil
	}
	out := make([]agentadaptor.Skill, 0, len(skills))
	for _, skill := range skills {
		out = append(out, cloneSkill(skill))
	}
	return out
}

func cloneSkill(skill agentadaptor.Skill) agentadaptor.Skill {
	return agentadaptor.Skill{
		Key:            skill.Key,
		Runtime:        skill.Runtime,
		Content:        skill.Content,
		PathHint:       skill.PathHint,
		Metadata:       cloneStringMap(skill.Metadata),
		Files:          append([]agentadaptor.SkillFile(nil), skill.Files...),
		Required:       skill.Required,
		RequiredReason: skill.RequiredReason,
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
