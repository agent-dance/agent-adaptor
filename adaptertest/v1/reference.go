package adaptertest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/agent-dance/agent-adaptor/driver"
)

// ReferenceConfig configures the in-memory reference driver returned by
// NewReferenceDriver.
type ReferenceConfig struct {
	// Model is the binding model; defaults to "reference-1".
	Model string
	// CWD is the fallback working directory when the request carries no
	// workspace lease.
	CWD string
	// FailRun makes Run report a provider-level failure (FailureAgentError
	// plus a run.error terminal frame) instead of a successful run.
	FailRun bool
}

// NewReferenceDriver returns the suite's reference driver.Driver: a fully
// in-memory implementation that satisfies every clause TestDriver enforces
// and implements all optional capability interfaces. It is both the suite's
// self-proof (see the package tests) and a template for third-party driver
// authors — every emission below is annotated with the clause it upholds.
func NewReferenceDriver(cfg ReferenceConfig) driver.Driver {
	return referenceDriver{cfg: cfg}
}

type referenceDriver struct {
	cfg ReferenceConfig
}

var (
	_ driver.Driver               = referenceDriver{}
	_ driver.EnvironmentProbe     = referenceDriver{}
	_ driver.ModelLister          = referenceDriver{}
	_ driver.ModelDetector        = referenceDriver{}
	_ driver.ProfileReporter      = referenceDriver{}
	_ driver.SessionCodecProvider = referenceDriver{}
	_ driver.ConfigSchemaProvider = referenceDriver{}
	_ driver.QuotaProbe           = referenceDriver{}
	_ driver.SkillSupport         = referenceDriver{}
	_ driver.StreamSupport        = referenceDriver{}
)

const referenceDriverType = "reference"

func (d referenceDriver) model() string {
	if d.cfg.Model != "" {
		return d.cfg.Model
	}
	return "reference-1"
}

// Descriptor declares exactly what the implementation delivers (CAP-01,
// CAP-02, SO-01): resume + codec, ephemeral skills, native and
// prompt-validate structured output working in the v1 execution pipeline and
// with the provider-native streaming transport.
func (d referenceDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{
		Type:        referenceDriverType,
		DisplayName: "Adaptertest Reference Driver",
		Models:      []driver.ModelInfo{{ID: d.model(), Label: "Reference Model"}},
		ConfigSchema: &driver.ConfigSchema{Fields: []driver.ConfigField{
			{Name: "model", Label: "Model", Type: "text", Group: "model", Default: "reference-1"},
			{Name: "cwd", Label: "Working directory", Type: "text", Group: "command"},
		}},
		Sessions:  driver.SessionCapability{SupportsResume: true},
		Skills:    driver.SkillCapability{Supported: true, Mode: driver.SkillSyncEphemeral},
		Workspace: driver.WorkspaceCapability{Supported: true},
		StructuredOutput: driver.StructuredOutputCapability{
			JSONSchemaNative:         true,
			JSONSchemaPromptValidate: true,
			WorksWithRun:             true,
			WorksWithStreaming:       true,
			Notes:                    "in-memory reference implementation",
		},
	}
}

// ValidateConfig accepts nil (validate the captured config), ReferenceConfig
// values and pointers, and rejects every foreign type (CFG-01..03).
func (d referenceDriver) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case nil, ReferenceConfig, *ReferenceConfig:
		return nil
	default:
		return fmt.Errorf("adaptertest reference driver: unsupported config type %T", cfg)
	}
}

type discardSink struct{}

func (discardSink) Emit(driver.RunEvent) error            { return nil }
func (discardSink) EmitStream(driver.StreamPayload) error { return nil }

// Run executes one deterministic in-memory turn. On the stream channel it
// emits a fully-braced lifecycle (EVT-01..11, EVT-13); on the event channel
// it mirrors every transcript item (RUN-02, RUN-04); the response satisfies
// RSP-01..05 and the TRN-* item rules.
func (d referenceDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	if err := ctx.Err(); err != nil {
		return driver.Response{}, err
	}
	if sink == nil {
		sink = discardSink{}
	}
	if req.Config != nil {
		if err := d.ValidateConfig(req.Config); err != nil {
			return driver.Response{}, err
		}
	}

	model := d.model()
	if req.ModelOverride != "" {
		// Request.ModelOverride docs: supersedes the binding model.
		model = req.ModelOverride
	}
	runID := req.RunID
	if runID == "" {
		runID = "reference-run"
	}
	sessionID := "reference-session"
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		sessionID = req.Session.State.ResumeID
	}
	cwd := req.Workspace.CWD
	if cwd == "" {
		cwd = d.cfg.CWD
	}
	if cwd == "" {
		cwd = "."
	}

	var transcript []driver.TranscriptItem
	addItem := func(item driver.TranscriptItem) {
		transcript = append(transcript, item)
		it := item
		// RUN-02: item events carry a non-nil Item. RUN-03/EVT-10: Seq and
		// Timestamp stay zero — the SDK backfills them.
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventItem, Item: &it})
	}
	stream := func(p driver.StreamPayload) {
		if !req.Streaming {
			// Request.Streaming is a hint; EmitStream is also legal when
			// disabled (the SDK discards it). Gating keeps intent obvious.
			return
		}
		p.RunID = runID
		p.ThreadID = sessionID
		_ = sink.EmitStream(p)
	}

	_ = sink.Emit(driver.RunEvent{
		Type:     driver.RunEventInvocation,
		Text:     "reference driver invocation",
		Metadata: map[string]string{"model": model, "cwd": cwd},
	})
	_ = sink.Emit(driver.RunEvent{Type: driver.RunEventSpawn, Text: "in-memory run (no child process)"})

	// EVT-01: run.started is the first normalized frame, exactly once.
	// EVT-13: run.* frames leave MessageID/ToolCallID empty.
	stream(driver.StreamPayload{Kind: driver.StreamRunStarted})

	addItem(driver.TranscriptItem{Kind: driver.TranscriptInit, Model: model, SessionID: sessionID})

	if d.cfg.FailRun {
		failure := &driver.RunFailure{
			Message: "reference driver was configured to fail this run",
			// RSP-02: agent_error carries no HumanDecision.
			Code: driver.FailureAgentError,
		}
		addItem(driver.TranscriptItem{
			Kind:     driver.TranscriptFailure,
			Text:     failure.Message,
			Metadata: map[string]string{"code": string(failure.Code)},
		})
		// EVT-02: run.error is the terminal frame and carries Error.
		stream(driver.StreamPayload{Kind: driver.StreamRunError, Error: failure})
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventLifecycle, Text: "run failed"})
		return driver.Response{
			RawStreams: &driver.RawStreams{},
			Transcript: transcript,
			ExitCode:   1,
			Provider:   referenceDriverType,
			Model:      model,
			Summary:    "reference run failed",
			Failure:    failure,
		}, nil
	}

	// Reasoning lifecycle (EVT-06): open -> content -> close.
	stream(driver.StreamPayload{Kind: driver.StreamReasoningStart, MessageID: "reason-1"})
	stream(driver.StreamPayload{Kind: driver.StreamReasoningContent, MessageID: "reason-1", Delta: "Planning the reply."})
	stream(driver.StreamPayload{Kind: driver.StreamReasoningEnd, MessageID: "reason-1"})
	addItem(driver.TranscriptItem{Kind: driver.TranscriptThinking, Text: "Planning the reply."})

	// Tool-call lifecycle (EVT-05): start(ToolCallID+Name) -> args(Delta)
	// -> end -> result.
	stream(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: "call-1", Name: "echo"})
	stream(driver.StreamPayload{Kind: driver.StreamToolCallArgs, ToolCallID: "call-1", Delta: `{"text":"ok"}`})
	stream(driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: "call-1"})
	stream(driver.StreamPayload{Kind: driver.StreamToolCallResult, ToolCallID: "call-1", Result: map[string]any{"text": "ok"}})
	addItem(driver.TranscriptItem{Kind: driver.TranscriptToolCall, ToolUseID: "call-1", ToolName: "echo", Input: map[string]any{"text": "ok"}})
	addItem(driver.TranscriptItem{Kind: driver.TranscriptToolResult, ToolUseID: "call-1", Text: "ok"})

	// Final assistant text; structured-output requests produce the exact
	// JSON value instead (SO-02).
	output := "Reference reply: " + req.Prompt
	var structured *driver.StructuredOutput
	if req.OutputSchema != nil {
		raw := json.RawMessage(`{"ok":true}`)
		output = string(raw)
		if req.OutputSchema.Mode != driver.StructuredOutputPromptValidate {
			// Native enforcement: the driver itself reports the validated
			// business value (StructuredOutput docs: RawJSON is never raw
			// stdout and never a provider terminal wrapper).
			structured = &driver.StructuredOutput{
				Format:     driver.OutputFormatJSONSchema,
				Mode:       req.OutputSchema.Mode,
				Source:     driver.StructuredOutputSourceNative,
				RawJSON:    raw,
				Value:      map[string]any{"ok": true},
				Valid:      true,
				SchemaHash: hashBytes(req.OutputSchema.SchemaJSON),
			}
		}
		// prompt_validate: the SDK injects instructions and validates the
		// final Output above the SPI; the driver only guarantees Output is
		// the exact JSON text.
	}

	// Text lifecycle (EVT-03/04): open -> token-level deltas -> close.
	stream(driver.StreamPayload{Kind: driver.StreamTextStart, MessageID: "msg-1"})
	half := len(output) / 2
	if half == 0 {
		half = len(output)
	}
	if half > 0 {
		stream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "msg-1", Delta: output[:half]})
	}
	if half < len(output) {
		stream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "msg-1", Delta: output[half:]})
	}
	stream(driver.StreamPayload{Kind: driver.StreamTextEnd, MessageID: "msg-1"})
	addItem(driver.TranscriptItem{Kind: driver.TranscriptAssistant, Text: output})

	usage := &driver.Usage{InputTokens: 12, OutputTokens: 34}
	addItem(driver.TranscriptItem{Kind: driver.TranscriptResult, Text: "success", Subtype: "success", Usage: usage})

	// EVT-11: every lifecycle above is closed before the terminal frame.
	// EVT-02: exactly one run.finished, nothing but closers may follow.
	stream(driver.StreamPayload{Kind: driver.StreamRunFinished, Usage: usage})
	_ = sink.Emit(driver.RunEvent{Type: driver.RunEventLifecycle, Text: "run completed"})

	// RSP-01: Valid checkpoint carries State with the provider handle.
	// Session guard data survives resume round-trips (SES-08 counterpart).
	data := map[string]string{driver.SessionParamCWD: cwd}
	if req.Session != nil && req.Session.State != nil {
		for k, v := range req.Session.State.Data {
			data[k] = v
		}
		data[driver.SessionParamCWD] = cwd
	}
	checkpoint := &driver.Checkpoint{
		Valid: true,
		State: &driver.SessionState{ResumeID: sessionID, DisplayID: sessionID, Data: data},
	}

	return driver.Response{
		// RSP-04: Output is final assistant-facing text only.
		Output: output,
		RawStreams: &driver.RawStreams{Terminal: &driver.TerminalPayload{
			Event: "result",
			JSON:  json.RawMessage(`{"subtype":"success"}`),
		}},
		Transcript:       transcript,
		ExitCode:         0,
		Usage:            usage,
		Checkpoint:       checkpoint,
		Provider:         referenceDriverType,
		Model:            model,
		Summary:          "reference run completed",
		StructuredOutput: structured,
	}, nil
}

func (d referenceDriver) CheckEnvironment(ctx context.Context, cfg any) (driver.EnvironmentReport, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return driver.EnvironmentReport{}, err
	}
	return driver.EnvironmentReport{
		DriverType: referenceDriverType,
		Status:     driver.EnvironmentPass,
		Healthy:    true,
		Summary:    "reference driver is always ready (in-memory, no CLI)",
		Checks: []driver.EnvironmentCheck{{
			Code:    "reference_ready",
			Level:   "pass",
			Message: "in-memory reference driver needs no external tooling",
		}},
	}, nil
}

func (d referenceDriver) ListModels(ctx context.Context, cfg any) ([]driver.ModelInfo, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	return []driver.ModelInfo{{ID: d.model(), Label: "Reference Model"}}, nil
}

func (d referenceDriver) DetectModel(ctx context.Context, cfg any, profile *driver.ProfileSelection) (*driver.DetectedModel, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	model := d.model()
	if rc, ok := cfg.(ReferenceConfig); ok && rc.Model != "" {
		model = rc.Model
	}
	return &driver.DetectedModel{Model: model, Provider: referenceDriverType, Source: "config"}, nil
}

func (d referenceDriver) GetProfile(ctx context.Context, cfg any, agent driver.AgentIdentity, profile *driver.ProfileSelection) (driver.AgentProfile, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return driver.AgentProfile{}, err
	}
	return driver.AgentProfile{
		DriverType: referenceDriverType,
		Supported:  true,
		Dir:        "adaptertest-reference-profile",
		EnvVar:     "ADAPTERTEST_REFERENCE_HOME",
		Source:     driver.AgentProfileSourceDefault,
	}, nil
}

func (d referenceDriver) GetQuota(ctx context.Context, cfg any, profile *driver.ProfileSelection) (driver.QuotaReport, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return driver.QuotaReport{}, err
	}
	return driver.QuotaReport{
		DriverType: referenceDriverType,
		Provider:   referenceDriverType,
		Source:     "static",
		Available:  true,
		Windows:    []driver.QuotaWindow{{Label: "session", ValueLabel: "unlimited"}},
	}, nil
}

func (d referenceDriver) ConfigSchema(ctx context.Context, cfg any) (*driver.ConfigSchema, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	schema := d.Descriptor().ConfigSchema
	return schema, nil
}

func (d referenceDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{
		Native:       true,
		TokenLevel:   true,
		Reasoning:    true,
		ToolCallArgs: true,
		HITL:         false,
	}
}

func (d referenceDriver) SessionCodec() driver.SessionCodec { return referenceCodec{} }

// ListSkills obeys the SkillSupport echo laws (CAP-09): the snapshot mirrors
// selected, keeps the full resolved catalogue (MUST NOT drop entries), and
// reports the payload fingerprint.
func (d referenceDriver) ListSkills(ctx context.Context, cfg any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, profile *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return driver.SkillSnapshot{}, err
	}
	return d.skillSnapshot(payload, selected, resolved), nil
}

func (d referenceDriver) InjectSkills(ctx context.Context, cfg any, payload driver.ResolvedSkills, profile *driver.ProfileSelection) error {
	return d.ValidateConfig(cfg)
}

func (d referenceDriver) SyncSkills(ctx context.Context, cfg any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, profile *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return driver.SkillSnapshot{}, err
	}
	return d.skillSnapshot(payload, selected, resolved), nil
}

func (d referenceDriver) skillSnapshot(payload driver.ResolvedSkills, selected []string, resolved []driver.Skill) driver.SkillSnapshot {
	mode := payload.Mode
	if mode == "" {
		mode = driver.SkillSyncEphemeral
	}
	entries := make([]driver.SnapshotEntry, 0, len(payload.Entries))
	for _, e := range payload.Entries {
		entries = append(entries, driver.SnapshotEntry{
			Key:         e.Key,
			RuntimeName: e.RuntimeName,
			Selected:    true,
			Required:    e.Required,
			State:       driver.SkillStateInstalled,
			Origin:      driver.SkillOriginManaged,
			SourcePath:  e.SourcePath,
		})
	}
	return driver.SkillSnapshot{
		DriverType:  referenceDriverType,
		Supported:   true,
		Mode:        mode,
		Selected:    append([]string(nil), selected...),
		Resolved:    append([]driver.Skill(nil), resolved...),
		Entries:     entries,
		Warnings:    append([]string(nil), payload.Warnings...),
		Fingerprint: payload.Fingerprint,
	}
}

// referenceCodec implements the canonical SessionCodec behavior the suite
// verifies (SES-01..08): nil state maps to zero params, zero params map to
// nil state, DisplayID falls back to ResumeID, and the guard fingerprint is
// a deterministic digest over ResumeID plus all session values.
type referenceCodec struct{}

func (referenceCodec) Name() string { return referenceDriverType }

func (referenceCodec) ToParams(state *driver.SessionState) driver.SessionParams {
	if state == nil {
		return driver.SessionParams{}
	}
	displayID := state.DisplayID
	if displayID == "" {
		displayID = state.ResumeID
	}
	return driver.SessionParams{
		ResumeID:  state.ResumeID,
		DisplayID: displayID,
		Values:    cloneStringMap(state.Data),
	}
}

func (referenceCodec) FromParams(params driver.SessionParams) *driver.SessionState {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return nil
	}
	displayID := params.DisplayID
	if displayID == "" {
		displayID = params.ResumeID
	}
	return &driver.SessionState{
		ResumeID:  params.ResumeID,
		DisplayID: displayID,
		Data:      cloneStringMap(params.Values),
	}
}

func (referenceCodec) GuardFingerprint(params driver.SessionParams) string {
	keys := make([]string, 0, len(params.Values))
	for k := range params.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte("resume_id=" + params.ResumeID + "\n"))
	for _, k := range keys {
		h.Write([]byte(k + "=" + params.Values[k] + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
