package adaptertest

import (
	"context"
	"reflect"
	"slices"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// Subject describes one adapter instance under conformance verification.
//
// The subject should use a config value that is safe to validate on the current
// machine. For adapters with local-home side effects, pass HOME/USERPROFILE in
// Config.CommonConfig.Env so the control-plane checks stay inside a test temp
// directory.
type Subject struct {
	Name string

	Adapter agentadaptor.DriverAdapter
	Config  any

	SessionState        *agentadaptor.DriverSessionState
	RequiredSessionKeys []string

	SkillPayload          agentadaptor.SkillPayload
	DesiredSkills         []string
	RequiredConfigFields  []string
	ExpectedDetectedModel string
}

// Run executes the reusable adapter conformance checks for the supplied
// subject. It focuses on SDK contract shape rather than provider-specific live
// execution.
func Run(t *testing.T, subject Subject) {
	t.Helper()

	if subject.Adapter == nil {
		t.Fatal("adaptertest.Subject.Adapter is required")
	}
	if subject.Name == "" {
		subject.Name = subject.Adapter.Descriptor().Type
	}

	descriptor := subject.Adapter.Descriptor()
	if descriptor.Type == "" {
		t.Fatalf("%s: descriptor type is required", subject.Name)
	}
	if descriptor.DisplayName == "" {
		t.Fatalf("%s: descriptor display name is required", subject.Name)
	}
	if err := subject.Adapter.ValidateConfig(subject.Config); err != nil {
		t.Fatalf("%s: validate config: %v", subject.Name, err)
	}

	checkModels(t, subject, descriptor)
	checkEnvironment(t, subject, descriptor)
	checkConfigSchema(t, subject, descriptor)
	checkDetectedModel(t, subject)
	checkProfile(t, subject, descriptor)
	checkQuota(t, subject, descriptor)
	checkSkills(t, subject, descriptor)
	checkSessionCodec(t, subject, descriptor)
}

func checkModels(t *testing.T, subject Subject, descriptor agentadaptor.DriverDescriptor) {
	t.Helper()

	models := append([]agentadaptor.ModelInfo(nil), descriptor.Models...)
	if aware, ok := subject.Adapter.(agentadaptor.ModelAwareDriver); ok {
		var err error
		models, err = aware.ListModels(context.Background(), subject.Config)
		if err != nil {
			t.Fatalf("%s: list models: %v", subject.Name, err)
		}
	}
	seen := map[string]struct{}{}
	for _, model := range models {
		if model.ID == "" {
			t.Fatalf("%s: model id is required", subject.Name)
		}
		if _, exists := seen[model.ID]; exists {
			t.Fatalf("%s: duplicate model id %q", subject.Name, model.ID)
		}
		seen[model.ID] = struct{}{}
	}
	if len(descriptor.Models) > 0 && len(models) == 0 {
		t.Fatalf("%s: descriptor declares models but ListModels returned none", subject.Name)
	}
}

func checkEnvironment(t *testing.T, subject Subject, descriptor agentadaptor.DriverDescriptor) {
	t.Helper()

	aware, ok := subject.Adapter.(agentadaptor.EnvironmentAwareDriver)
	if !ok {
		return
	}
	report, err := aware.CheckEnvironment(context.Background(), subject.Config)
	if err != nil {
		t.Fatalf("%s: check environment: %v", subject.Name, err)
	}
	if report.DriverType != descriptor.Type {
		t.Fatalf("%s: unexpected environment driver type %q", subject.Name, report.DriverType)
	}
	switch report.Status {
	case agentadaptor.EnvironmentPass, agentadaptor.EnvironmentWarn, agentadaptor.EnvironmentFail:
	default:
		t.Fatalf("%s: invalid environment status %q", subject.Name, report.Status)
	}
	if report.Summary == "" {
		t.Fatalf("%s: environment summary is required", subject.Name)
	}
}

func checkConfigSchema(t *testing.T, subject Subject, descriptor agentadaptor.DriverDescriptor) {
	t.Helper()

	schema := descriptor.ConfigSchema
	if aware, ok := subject.Adapter.(agentadaptor.ConfigSchemaAwareDriver); ok {
		resolved, err := aware.ConfigSchema(context.Background(), subject.Config)
		if err != nil {
			t.Fatalf("%s: config schema: %v", subject.Name, err)
		}
		schema = resolved
	}
	if schema == nil {
		if len(subject.RequiredConfigFields) > 0 {
			t.Fatalf("%s: required config fields declared but adapter exposes no schema", subject.Name)
		}
		return
	}
	fieldNames := make([]string, 0, len(schema.Fields))
	seen := map[string]struct{}{}
	for _, field := range schema.Fields {
		if field.Name == "" {
			t.Fatalf("%s: config field name is required", subject.Name)
		}
		if field.Type == "" {
			t.Fatalf("%s: config field %q type is required", subject.Name, field.Name)
		}
		if _, exists := seen[field.Name]; exists {
			t.Fatalf("%s: duplicate config field %q", subject.Name, field.Name)
		}
		seen[field.Name] = struct{}{}
		fieldNames = append(fieldNames, field.Name)
		optionValues := map[string]struct{}{}
		for _, option := range field.Options {
			if option.Value == "" {
				t.Fatalf("%s: config field %q has empty option value", subject.Name, field.Name)
			}
			if _, exists := optionValues[option.Value]; exists {
				t.Fatalf("%s: config field %q has duplicate option value %q", subject.Name, field.Name, option.Value)
			}
			optionValues[option.Value] = struct{}{}
		}
		if len(field.Options) > 0 {
			if defaultValue, ok := field.Default.(string); ok && defaultValue != "" {
				if _, exists := optionValues[defaultValue]; !exists {
					t.Fatalf("%s: config field %q default %q is not present in options", subject.Name, field.Name, defaultValue)
				}
			}
		}
	}
	for _, required := range subject.RequiredConfigFields {
		if !slices.Contains(fieldNames, required) {
			t.Fatalf("%s: missing required config field %q", subject.Name, required)
		}
	}
}

func checkDetectedModel(t *testing.T, subject Subject) {
	t.Helper()

	detector, ok := subject.Adapter.(agentadaptor.ModelDetectorDriver)
	if !ok {
		return
	}
	detected, err := detector.DetectModel(context.Background(), subject.Config, nil)
	if err != nil {
		t.Fatalf("%s: detect model: %v", subject.Name, err)
	}
	if detected == nil {
		if subject.ExpectedDetectedModel != "" {
			t.Fatalf("%s: expected detected model %q, got nil", subject.Name, subject.ExpectedDetectedModel)
		}
		return
	}
	if detected.Model == "" || detected.Source == "" {
		t.Fatalf("%s: detected model must include model and source, got %#v", subject.Name, detected)
	}
	if subject.ExpectedDetectedModel != "" && detected.Model != subject.ExpectedDetectedModel {
		t.Fatalf("%s: expected detected model %q, got %#v", subject.Name, subject.ExpectedDetectedModel, detected)
	}
}

func checkProfile(t *testing.T, subject Subject, descriptor agentadaptor.DriverDescriptor) {
	t.Helper()

	aware, ok := subject.Adapter.(agentadaptor.ProfileAwareDriver)
	if !ok {
		return
	}
	profile, err := aware.GetProfile(context.Background(), subject.Config, agentadaptor.AgentIdentity{}, nil)
	if err != nil {
		t.Fatalf("%s: get profile: %v", subject.Name, err)
	}
	if profile.DriverType != descriptor.Type {
		t.Fatalf("%s: unexpected profile driver type %q", subject.Name, profile.DriverType)
	}
	if !profile.Supported {
		t.Fatalf("%s: profile-aware adapter returned unsupported profile %#v", subject.Name, profile)
	}
	if profile.Dir == "" {
		t.Fatalf("%s: supported profile report must include dir", subject.Name)
	}
	if profile.EnvVar == "" {
		t.Fatalf("%s: supported profile report must include env var", subject.Name)
	}
	switch profile.Source {
	case agentadaptor.AgentProfileSourceBindingEnv,
		agentadaptor.AgentProfileSourceProfileOption,
		agentadaptor.AgentProfileSourceProcessEnv,
		agentadaptor.AgentProfileSourceDefault,
		agentadaptor.AgentProfileSourceManaged:
	default:
		t.Fatalf("%s: invalid profile source %q", subject.Name, profile.Source)
	}
}

func checkQuota(t *testing.T, subject Subject, descriptor agentadaptor.DriverDescriptor) {
	t.Helper()

	aware, ok := subject.Adapter.(agentadaptor.QuotaAwareDriver)
	if !ok {
		return
	}
	report, err := aware.GetQuota(context.Background(), subject.Config, nil)
	if err != nil {
		t.Fatalf("%s: get quota: %v", subject.Name, err)
	}
	if report.DriverType != descriptor.Type {
		t.Fatalf("%s: unexpected quota driver type %q", subject.Name, report.DriverType)
	}
	for _, window := range report.Windows {
		if window.Label == "" {
			t.Fatalf("%s: quota window label is required", subject.Name)
		}
	}
}

func checkSkills(t *testing.T, subject Subject, descriptor agentadaptor.DriverDescriptor) {
	t.Helper()

	if !descriptor.Skills.Supported {
		return
	}
	driver, ok := subject.Adapter.(agentadaptor.SkillAwareDriver)
	if !ok {
		t.Fatalf("%s: skills are supported but adapter does not implement SkillAwareDriver", subject.Name)
	}
	payload := subject.SkillPayload
	if payload.Mode == "" {
		payload.Mode = descriptor.Skills.Mode
	}
	listed, err := driver.ListSkills(context.Background(), subject.Config, payload, nil)
	if err != nil {
		t.Fatalf("%s: list skills: %v", subject.Name, err)
	}
	if listed.DriverType != descriptor.Type || !listed.Supported || listed.Mode != descriptor.Skills.Mode {
		t.Fatalf("%s: invalid list skills snapshot %#v", subject.Name, listed)
	}
	if !reflect.DeepEqual(listed.Desired, payload.Requested) {
		t.Fatalf("%s: list skills desired mismatch, want %#v got %#v", subject.Name, payload.Requested, listed.Desired)
	}
	desired := append([]string(nil), subject.DesiredSkills...)
	if len(desired) == 0 {
		desired = append([]string(nil), payload.Requested...)
	}
	synced, err := driver.SyncSkills(context.Background(), subject.Config, payload, desired, nil)
	if err != nil {
		t.Fatalf("%s: sync skills: %v", subject.Name, err)
	}
	if synced.DriverType != descriptor.Type || !synced.Supported || synced.Mode != descriptor.Skills.Mode {
		t.Fatalf("%s: invalid sync skills snapshot %#v", subject.Name, synced)
	}
	if !reflect.DeepEqual(synced.Desired, desired) {
		t.Fatalf("%s: sync skills desired mismatch, want %#v got %#v", subject.Name, desired, synced.Desired)
	}
}

func checkSessionCodec(t *testing.T, subject Subject, descriptor agentadaptor.DriverDescriptor) {
	t.Helper()

	if !descriptor.Sessions.SupportsResume {
		return
	}
	if _, ok := subject.Adapter.(agentadaptor.SessionCodecAwareDriver); !ok {
		t.Fatalf("%s: resume-capable adapters must implement SessionCodecAwareDriver", subject.Name)
	}

	state := subject.SessionState
	if state == nil {
		state = &agentadaptor.DriverSessionState{
			ResumeID: "resume-session",
			Data:     map[string]string{},
		}
	}
	codec := agentadaptor.SessionCodecFor(subject.Adapter)
	params := codec.ToParams(state)
	if params.ResumeID == "" {
		t.Fatalf("%s: session codec must preserve resume id", subject.Name)
	}
	if params.DisplayID == "" {
		t.Fatalf("%s: session codec must provide a display id", subject.Name)
	}
	for _, key := range subject.RequiredSessionKeys {
		if state.Data[key] != params.Values[key] {
			t.Fatalf("%s: session codec lost key %q", subject.Name, key)
		}
	}
	restored := codec.FromParams(params)
	if restored == nil {
		t.Fatalf("%s: session codec must restore state", subject.Name)
	}
	roundTrip := codec.ToParams(restored)
	if !reflect.DeepEqual(params, roundTrip) {
		t.Fatalf("%s: session codec round-trip mismatch, want %#v got %#v", subject.Name, params, roundTrip)
	}
	firstGuard := codec.GuardFingerprint(params)
	secondGuard := codec.GuardFingerprint(roundTrip)
	if firstGuard == "" || firstGuard != secondGuard {
		t.Fatalf("%s: session guard fingerprint must be stable, got %q and %q", subject.Name, firstGuard, secondGuard)
	}
}
