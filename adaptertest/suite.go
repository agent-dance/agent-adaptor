package adaptertest

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// DefaultLivePrompt is used by WithLiveRun("").
const DefaultLivePrompt = "Reply with exactly: OK"

const (
	probeTimeout       = 30 * time.Second
	defaultLiveTimeout = 5 * time.Minute
)

// Option configures TestDriver.
type Option func(*suiteConfig)

type suiteConfig struct {
	config               any
	sessionState         *driver.SessionState
	sessionKeys          []string
	guardKeys            []string
	workspaceCWD         string
	expectedModel        string
	requiredConfigFields []string
	rejectForeign        bool
	syncSkillsProbe      bool
	livePrompt           string
	liveTimeout          time.Duration
	liveSkipReason       string
	liveStructured       bool
}

// WithConfig supplies the driver-specific config value passed to every
// probe (ValidateConfig, CheckEnvironment, ListModels, ...) and to live
// runs via Request.Config. v1 drivers built with <pkg>.Driver(cfg) may
// receive nil here; they inject their captured config themselves.
func WithConfig(cfg any) Option { return func(c *suiteConfig) { c.config = cfg } }

// WithSessionState seeds the session-codec round-trip probes (SES-*). When
// omitted the suite uses a generic resumable state.
func WithSessionState(state *driver.SessionState) Option {
	return func(c *suiteConfig) { c.sessionState = state }
}

// WithSessionKeys lists SessionState.Data keys that must survive the codec
// round-trip (SES-08). Keys must be present in the seeded session state.
func WithSessionKeys(keys ...string) Option {
	return func(c *suiteConfig) { c.sessionKeys = append(c.sessionKeys, keys...) }
}

// WithGuardKeys lists session parameter keys that participate in the
// codec's GuardFingerprint; mutating any of them must change the
// fingerprint (SES-06). Without this option SES-06 is skipped, because
// which keys are guard-relevant is driver-specific.
func WithGuardKeys(keys ...string) Option {
	return func(c *suiteConfig) { c.guardKeys = append(c.guardKeys, keys...) }
}

// WithWorkspace sets the workspace CWD used for live runs.
func WithWorkspace(cwd string) Option { return func(c *suiteConfig) { c.workspaceCWD = cwd } }

// WithExpectedDetectedModel asserts the ModelDetector result (CAP-05).
func WithExpectedDetectedModel(model string) Option {
	return func(c *suiteConfig) { c.expectedModel = model }
}

// WithRequiredConfigFields asserts that the hydrated ConfigSchema contains
// these field names (CAP-08).
func WithRequiredConfigFields(names ...string) Option {
	return func(c *suiteConfig) { c.requiredConfigFields = append(c.requiredConfigFields, names...) }
}

// ExpectRejectForeignConfig asserts that ValidateConfig rejects a config
// value of a type the driver has never seen (CFG-03). All built-in drivers
// satisfy this; it is opt-in because the SPI does not mandate it.
func ExpectRejectForeignConfig() Option { return func(c *suiteConfig) { c.rejectForeign = true } }

// WithSyncSkillsProbe additionally exercises SyncSkills with the empty
// catalogue (CAP-09). Opt-in because SyncSkills reconciles host-side state
// and may write into the driver profile directory.
func WithSyncSkillsProbe() Option { return func(c *suiteConfig) { c.syncSkillsProbe = true } }

// WithLiveRun enables the live execution probes (EVT-*, RUN-*, TRN-*,
// RSP-*): the suite invokes Run against the real provider with prompt.
// An empty prompt selects DefaultLivePrompt. Callers gate this on CLI
// availability; see SkipLiveRun.
func WithLiveRun(prompt string) Option {
	return func(c *suiteConfig) {
		if prompt == "" {
			prompt = DefaultLivePrompt
		}
		c.livePrompt = prompt
	}
}

// WithLiveRunTimeout overrides the live run deadline (default 5 minutes).
func WithLiveRunTimeout(d time.Duration) Option {
	return func(c *suiteConfig) { c.liveTimeout = d }
}

// SkipLiveRun records why the live probes are skipped (for example
// "codex CLI not in PATH") and wins over WithLiveRun.
func SkipLiveRun(reason string) Option {
	return func(c *suiteConfig) {
		if reason == "" {
			reason = "live run skipped"
		}
		c.liveSkipReason = reason
	}
}

// WithLiveStructuredOutput additionally runs the native structured-output
// probe (SO-02) when the descriptor declares JSONSchemaNative for Run. The
// suite never sends a mode the descriptor does not declare (SO-03).
func WithLiveStructuredOutput() Option { return func(c *suiteConfig) { c.liveStructured = true } }

// foreignConfig is a type no driver under test can know about (CFG-03).
type foreignConfig struct{ marker string }

// TestDriver runs the v1 driver conformance suite against drivers produced
// by newDriver, in the style of fstest.TestFS / nettest.TestConn. Probes
// for optional capability interfaces skip when the driver does not
// implement them; capability declarations in the Descriptor are
// cross-checked against the implemented interfaces (truthfulness), and the
// suite never probes a capability the descriptor does not declare.
// Failure messages carry the numbered contract clauses catalogued in the
// package documentation.
func TestDriver(t *testing.T, newDriver func() driver.Driver, opts ...Option) {
	t.Helper()
	if newDriver == nil {
		t.Fatal("adaptertest: newDriver factory is required")
	}
	c := &suiteConfig{liveTimeout: defaultLiveTimeout}
	for _, opt := range opts {
		opt(c)
	}
	if c.sessionState == nil {
		c.sessionState = &driver.SessionState{
			ResumeID: "adaptertest-session",
			Data:     map[string]string{"adaptertest_probe": "1"},
		}
	}

	d := newDriver()
	if d == nil {
		t.Fatal("DRV-01: newDriver returned a nil driver")
	}
	desc := d.Descriptor()

	t.Run("descriptor", func(t *testing.T) { checkDescriptor(t, newDriver, d, desc) })
	t.Run("config", func(t *testing.T) { checkConfig(t, d, c) })
	t.Run("capability_declarations", func(t *testing.T) { checkDeclarations(t, newDriver, d, desc) })
	t.Run("environment", func(t *testing.T) { checkEnvironmentProbe(t, d, desc, c) })
	t.Run("models", func(t *testing.T) { checkModelLister(t, d, c) })
	t.Run("detect_model", func(t *testing.T) { checkModelDetector(t, d, c) })
	t.Run("profile", func(t *testing.T) { checkProfileReporter(t, d, desc, c) })
	t.Run("quota", func(t *testing.T) { checkQuotaProbe(t, d, desc, c) })
	t.Run("config_schema", func(t *testing.T) { checkConfigSchemaProvider(t, d, c) })
	t.Run("skills", func(t *testing.T) { checkSkillSupport(t, d, desc, c) })
	t.Run("stream_capability", func(t *testing.T) { checkStreamSupport(t, d) })
	t.Run("session_codec", func(t *testing.T) { checkSessionCodec(t, d, desc, c) })
	t.Run("live_run", func(t *testing.T) { checkLiveRun(t, d, c) })
	t.Run("live_structured_output", func(t *testing.T) { checkLiveStructuredOutput(t, d, desc, c) })
}

func probeContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	t.Cleanup(cancel)
	return ctx
}

func reportViolations(t *testing.T, violations []Violation) {
	t.Helper()
	const maxReported = 25
	for i, v := range violations {
		if i == maxReported {
			t.Errorf("... and %d more violations", len(violations)-maxReported)
			return
		}
		t.Errorf("%s", v)
	}
}

func mustNotPanic(t *testing.T, clause, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: %s panicked: %v", clause, what, r)
		}
	}()
	fn()
}

func checkDescriptor(t *testing.T, newDriver func() driver.Driver, d driver.Driver, desc driver.Descriptor) {
	t.Helper()
	if desc.Type == "" {
		t.Error("DRV-01: Descriptor().Type is empty; the descriptor is the driver's static capability declaration (driver.Descriptor docs)")
	}
	if desc.DisplayName == "" {
		t.Error("DRV-01: Descriptor().DisplayName is empty")
	}
	if again := d.Descriptor(); !reflect.DeepEqual(again, desc) {
		t.Error("DRV-02: Descriptor() is not deterministic across calls on the same instance")
	}
	fresh := newDriver()
	if fresh == nil {
		t.Fatal("DRV-02: newDriver returned nil on a second invocation")
	}
	if fd := fresh.Descriptor(); !reflect.DeepEqual(fd, desc) {
		t.Error("DRV-02: Descriptor() differs across factory instances; the declaration must be static")
	}
}

func checkConfig(t *testing.T, d driver.Driver, c *suiteConfig) {
	t.Helper()
	mustNotPanic(t, "CFG-01", "ValidateConfig(nil)", func() {
		if err := d.ValidateConfig(nil); err != nil {
			t.Logf("note: ValidateConfig(nil) = %v (v1 drivers validate their captured config on nil)", err)
		}
	})
	if c.config != nil {
		if err := d.ValidateConfig(c.config); err != nil {
			t.Errorf("CFG-02: ValidateConfig rejected the suite-supplied config: %v", err)
		}
	}
	if c.rejectForeign {
		if err := d.ValidateConfig(foreignConfig{marker: "adaptertest"}); err == nil {
			t.Error("CFG-03: ValidateConfig accepted a foreign config type; drivers own provider-specific validation (driver.Driver docs)")
		}
	}
}

func checkDeclarations(t *testing.T, newDriver func() driver.Driver, d driver.Driver, desc driver.Descriptor) {
	t.Helper()
	resumeViolations := VerifySessionCapability(d)
	reportViolations(t, resumeViolations)
	reportViolations(t, VerifyProcessCapability(d))
	if desc.Sessions.SupportsResume {
		fresh := newDriver()
		if fresh == nil {
			t.Error("CAP-01: newDriver returned nil while checking resume identity stability")
		} else {
			freshViolations := VerifySessionCapability(fresh)
			reportViolations(t, freshViolations)
			if len(resumeViolations) == 0 && len(freshViolations) == 0 {
				firstCodec, _ := callSessionCodec(d.(driver.SessionCodecProvider))
				freshCodec, _ := callSessionCodec(fresh.(driver.SessionCodecProvider))
				firstName, _ := callCodecName(firstCodec)
				freshName, _ := callCodecName(freshCodec)
				if firstName != freshName {
					t.Errorf("CAP-01: SessionCodec.Name() differs across factory instances: %q then %q", firstName, freshName)
				}
				firstFingerprint, _, _ := callConfigFingerprint(d.(driver.SessionConfigFingerprinter))
				freshFingerprint, _, _ := callConfigFingerprint(fresh.(driver.SessionConfigFingerprinter))
				if firstFingerprint != freshFingerprint {
					t.Error("CAP-01: SessionConfigFingerprint() differs across equivalent factory instances")
				}
			}
		}
	}
	// CAP-02: skills declaration implies SkillSupport and a coherent mode.
	if desc.Skills.Supported {
		if _, ok := d.(driver.SkillSupport); !ok {
			t.Error("CAP-02: Skills.Supported=true but the driver does not implement driver.SkillSupport")
		}
		if desc.Skills.Mode != driver.SkillSyncEphemeral && desc.Skills.Mode != driver.SkillSyncPersistent {
			t.Errorf("CAP-02: Skills.Supported=true with Mode=%q, want %q or %q", desc.Skills.Mode, driver.SkillSyncEphemeral, driver.SkillSyncPersistent)
		}
	} else if desc.Skills.Mode == driver.SkillSyncEphemeral || desc.Skills.Mode == driver.SkillSyncPersistent {
		t.Errorf("CAP-02: Skills.Supported=false but Mode=%q declares a sync mode", desc.Skills.Mode)
	}
	reportViolations(t, VerifyStructuredOutputCapability(desc.StructuredOutput))
}

func checkEnvironmentProbe(t *testing.T, d driver.Driver, desc driver.Descriptor, c *suiteConfig) {
	t.Helper()
	probe, ok := d.(driver.EnvironmentProbe)
	if !ok {
		t.Skip("driver does not implement driver.EnvironmentProbe")
	}
	report, err := probe.CheckEnvironment(probeContext(t), c.config)
	if err != nil {
		t.Fatalf("CAP-03: CheckEnvironment returned an error for the hermetic probe: %v (preflight problems belong in the report Status, not the error)", err)
	}
	if report.DriverType != desc.Type {
		t.Errorf("CAP-03: EnvironmentReport.DriverType = %q, want descriptor type %q", report.DriverType, desc.Type)
	}
	switch report.Status {
	case driver.EnvironmentPass, driver.EnvironmentWarn, driver.EnvironmentFail:
	default:
		t.Errorf("CAP-03: EnvironmentReport.Status = %q, want pass|warn|fail", report.Status)
	}
	if report.Status == driver.EnvironmentPass && !report.Healthy {
		t.Error("CAP-03: Status=pass with Healthy=false; Healthy is the backward-compatible mirror of Status (EnvironmentReport docs)")
	}
	for i, check := range report.Checks {
		if check.Code == "" {
			t.Errorf("CAP-03: EnvironmentCheck %d has empty Code; Code is the stable machine-facing identifier", i)
		}
	}
}

func checkModelLister(t *testing.T, d driver.Driver, c *suiteConfig) {
	t.Helper()
	lister, ok := d.(driver.ModelLister)
	if !ok {
		t.Skip("driver does not implement driver.ModelLister")
	}
	models, err := lister.ListModels(probeContext(t), c.config)
	if err != nil {
		t.Fatalf("CAP-04: ListModels returned an error for the hermetic probe: %v", err)
	}
	for i, m := range models {
		if m.ID == "" {
			t.Errorf("CAP-04: ListModels entry %d has empty ID (ModelInfo docs: ID is the value accepted by config)", i)
		}
	}
}

func checkModelDetector(t *testing.T, d driver.Driver, c *suiteConfig) {
	t.Helper()
	detector, ok := d.(driver.ModelDetector)
	if !ok {
		t.Skip("driver does not implement driver.ModelDetector")
	}
	detected, err := detector.DetectModel(probeContext(t), c.config, nil)
	if err != nil {
		t.Fatalf("CAP-05: DetectModel returned an error for the hermetic probe: %v", err)
	}
	if detected != nil && detected.Model == "" {
		t.Error("CAP-05: DetectModel returned a non-nil result with empty Model")
	}
	if c.expectedModel != "" {
		if detected == nil {
			t.Errorf("CAP-05: DetectModel returned nil, want model %q", c.expectedModel)
		} else if detected.Model != c.expectedModel {
			t.Errorf("CAP-05: DetectModel = %q, want %q", detected.Model, c.expectedModel)
		}
	}
}

func checkProfileReporter(t *testing.T, d driver.Driver, desc driver.Descriptor, c *suiteConfig) {
	t.Helper()
	reporter, ok := d.(driver.ProfileReporter)
	if !ok {
		t.Skip("driver does not implement driver.ProfileReporter")
	}
	profile, err := reporter.GetProfile(probeContext(t), c.config, driver.AgentIdentity{}, nil)
	if err != nil {
		t.Fatalf("CAP-06: GetProfile returned an error for the hermetic probe: %v", err)
	}
	if profile.DriverType != desc.Type {
		t.Errorf("CAP-06: AgentProfile.DriverType = %q, want descriptor type %q", profile.DriverType, desc.Type)
	}
	if profile.Supported {
		if profile.Dir == "" {
			t.Error("CAP-06: AgentProfile.Supported=true with empty Dir (AgentProfile docs: Dir is the effective directory)")
		}
		if profile.Source == "" {
			t.Error("CAP-06: AgentProfile.Supported=true with empty Source")
		}
	}
}

func checkQuotaProbe(t *testing.T, d driver.Driver, desc driver.Descriptor, c *suiteConfig) {
	t.Helper()
	probe, ok := d.(driver.QuotaProbe)
	if !ok {
		t.Skip("driver does not implement driver.QuotaProbe")
	}
	report, err := probe.GetQuota(probeContext(t), c.config, nil)
	if err != nil {
		t.Fatalf("CAP-07: GetQuota returned an error for the hermetic probe: %v (unavailability belongs in Available/Error, not the error)", err)
	}
	if report.DriverType != desc.Type {
		t.Errorf("CAP-07: QuotaReport.DriverType = %q, want descriptor type %q", report.DriverType, desc.Type)
	}
	for i, window := range report.Windows {
		if window.Label == "" {
			t.Errorf("CAP-07: QuotaWindow %d has empty Label", i)
		}
	}
}

func checkConfigSchemaProvider(t *testing.T, d driver.Driver, c *suiteConfig) {
	t.Helper()
	provider, ok := d.(driver.ConfigSchemaProvider)
	if !ok {
		t.Skip("driver does not implement driver.ConfigSchemaProvider")
	}
	schema, err := provider.ConfigSchema(probeContext(t), c.config)
	if err != nil {
		t.Fatalf("CAP-08: ConfigSchema returned an error for the hermetic probe: %v", err)
	}
	if schema == nil {
		t.Fatal("CAP-08: ConfigSchema returned a nil schema without error")
	}
	seen := map[string]bool{}
	for i, field := range schema.Fields {
		if field.Name == "" {
			t.Errorf("CAP-08: ConfigField %d has empty Name", i)
			continue
		}
		if seen[field.Name] {
			t.Errorf("CAP-08: duplicate ConfigField name %q", field.Name)
		}
		seen[field.Name] = true
	}
	for _, required := range c.requiredConfigFields {
		if !seen[required] {
			t.Errorf("CAP-08: ConfigSchema is missing required field %q", required)
		}
	}
}

func checkSkillSupport(t *testing.T, d driver.Driver, desc driver.Descriptor, c *suiteConfig) {
	t.Helper()
	if !desc.Skills.Supported {
		t.Skip("descriptor declares skills unsupported; the suite does not probe undeclared capabilities")
	}
	support, ok := d.(driver.SkillSupport)
	if !ok {
		t.Skip("SkillSupport not implemented (already reported as CAP-02)")
	}
	payload := driver.ResolvedSkills{Mode: desc.Skills.Mode}
	snapshot, err := support.ListSkills(probeContext(t), c.config, payload, nil, nil, nil)
	if err != nil {
		t.Fatalf("CAP-09: ListSkills returned an error for the empty-catalogue probe: %v", err)
	}
	checkSkillSnapshot(t, "ListSkills", snapshot, desc)
	if c.syncSkillsProbe {
		snapshot, err := support.SyncSkills(probeContext(t), c.config, payload, nil, nil, nil)
		if err != nil {
			t.Fatalf("CAP-09: SyncSkills returned an error for the empty-catalogue probe: %v", err)
		}
		checkSkillSnapshot(t, "SyncSkills", snapshot, desc)
	}
}

func checkSkillSnapshot(t *testing.T, method string, snapshot driver.SkillSnapshot, desc driver.Descriptor) {
	t.Helper()
	if !snapshot.Supported {
		t.Errorf("CAP-09: %s snapshot reports Supported=false although the descriptor declares skill support", method)
	}
	if snapshot.DriverType != desc.Type {
		t.Errorf("CAP-09: %s snapshot DriverType = %q, want descriptor type %q", method, snapshot.DriverType, desc.Type)
	}
	if snapshot.Mode != driver.SkillSyncEphemeral && snapshot.Mode != driver.SkillSyncPersistent {
		t.Errorf("CAP-09: %s snapshot Mode = %q, want %q or %q", method, snapshot.Mode, driver.SkillSyncEphemeral, driver.SkillSyncPersistent)
	}
	if len(snapshot.Selected) != 0 {
		t.Errorf("CAP-09: %s snapshot Selected = %v for the empty selection; SkillSupport docs: selected == payload.Keys()", method, snapshot.Selected)
	}
}

func checkStreamSupport(t *testing.T, d driver.Driver) {
	t.Helper()
	support, ok := d.(driver.StreamSupport)
	if !ok {
		t.Skip("driver does not implement driver.StreamSupport")
	}
	first := support.StreamCapability()
	if second := support.StreamCapability(); second != first {
		t.Errorf("CAP-10: StreamCapability() is not deterministic: %+v then %+v", first, second)
	}
}

func checkSessionCodec(t *testing.T, d driver.Driver, desc driver.Descriptor, c *suiteConfig) {
	t.Helper()
	provider, ok := d.(driver.SessionCodecProvider)
	if !ok {
		if desc.Sessions.SupportsResume {
			t.Fatal("CAP-01: resume-capable driver without SessionCodecProvider (also reported under capability_declarations)")
		}
		t.Skip("driver does not implement driver.SessionCodecProvider")
	}
	codec, panicked := callSessionCodec(provider)
	if panicked {
		t.Fatal("CAP-01: SessionCodec() panicked")
	}
	if nilDynamicValue(codec) {
		t.Fatal("CAP-01: SessionCodec() returned nil or a typed-nil codec")
	}

	// SES-01: stable non-empty name.
	name, ok := callCodecName(codec)
	if !ok {
		t.Fatal("SES-01: SessionCodec.Name() panicked")
	}
	if name == "" {
		t.Error("SES-01: SessionCodec.Name() is empty")
	}
	if again, ok := callCodecName(codec); !ok {
		t.Error("SES-01: SessionCodec.Name() panicked on its second call")
	} else if again != name {
		t.Errorf("SES-01: SessionCodec.Name() unstable: %q then %q", name, again)
	}

	// SES-07: nil/zero inputs never panic.
	mustNotPanic(t, "SES-07", "ToParams(nil)", func() { _ = codec.ToParams(nil) })
	mustNotPanic(t, "SES-07", "FromParams(zero)", func() { _ = codec.FromParams(driver.SessionParams{}) })
	mustNotPanic(t, "SES-07", "GuardFingerprint(zero)", func() { _ = codec.GuardFingerprint(driver.SessionParams{}) })

	// SES-02: canonical nil/zero mapping.
	if zero := codec.ToParams(nil); !reflect.DeepEqual(zero, driver.SessionParams{}) {
		t.Errorf("SES-02: ToParams(nil) = %+v, want the zero SessionParams", zero)
	}
	if state := codec.FromParams(driver.SessionParams{}); state != nil {
		t.Errorf("SES-02: FromParams(zero) = %+v, want nil (no session to restore)", state)
	}

	seed := cloneSessionState(c.sessionState)
	params := codec.ToParams(seed)

	// SES-03: identity and data preservation.
	if params.ResumeID != seed.ResumeID {
		t.Errorf("SES-03: ToParams dropped ResumeID: got %q, want %q", params.ResumeID, seed.ResumeID)
	}
	if seed.ResumeID != "" && params.DisplayID == "" {
		t.Error("SES-03: ToParams left DisplayID empty for a resumable state; DisplayID is the user-facing label (SessionParams docs)")
	}
	for key, want := range seed.Data {
		if got, ok := params.Values[key]; !ok || got != want {
			t.Errorf("SES-03: ToParams lost Data[%q]: got %q (present=%v), want %q", key, got, ok, want)
		}
	}

	// SES-04: params -> state -> params is lossless.
	restored := codec.FromParams(params)
	if restored == nil {
		t.Fatal("SES-04: FromParams returned nil for a resumable params value")
	}
	if roundTrip := codec.ToParams(restored); !reflect.DeepEqual(roundTrip, params) {
		t.Errorf("SES-04: codec round-trip is lossy:\n first = %+v\nsecond = %+v", params, roundTrip)
	}

	// SES-08: required session keys survive the round-trip.
	for _, key := range c.sessionKeys {
		want, ok := seed.Data[key]
		if !ok {
			t.Errorf("SES-08: suite misconfiguration: session key %q missing from the seeded state", key)
			continue
		}
		if got := params.Values[key]; got != want {
			t.Errorf("SES-08: params.Values[%q] = %q, want %q", key, got, want)
		}
		if got := restored.Data[key]; got != want {
			t.Errorf("SES-08: restored.Data[%q] = %q, want %q", key, got, want)
		}
	}

	// SES-05: guard fingerprint non-empty and deterministic.
	fingerprint := codec.GuardFingerprint(params)
	if fingerprint == "" {
		t.Error("SES-05: GuardFingerprint is empty for a resumable state")
	}
	if again := codec.GuardFingerprint(params); again != fingerprint {
		t.Errorf("SES-05: GuardFingerprint unstable: %q then %q", fingerprint, again)
	}
	if again := codec.GuardFingerprint(codec.ToParams(cloneSessionState(c.sessionState))); again != fingerprint {
		t.Errorf("SES-05: GuardFingerprint differs across equivalent ToParams calls: %q then %q", fingerprint, again)
	}

	// SES-06: mutating a guard-relevant value must change the fingerprint
	// (SessionCodec docs: the guard changes whenever the guarded state
	// changes, so stale resumes can be rejected).
	if len(c.guardKeys) == 0 {
		t.Log("SES-06 skipped: no WithGuardKeys configured (guard-relevant keys are driver-specific)")
	}
	for _, key := range c.guardKeys {
		mutated := driver.SessionParams{
			ResumeID:  params.ResumeID,
			DisplayID: params.DisplayID,
			Values:    map[string]string{},
		}
		for k, v := range params.Values {
			mutated.Values[k] = v
		}
		mutated.Values[key] = mutated.Values[key] + "-adaptertest-mutated"
		if codec.GuardFingerprint(mutated) == fingerprint {
			t.Errorf("SES-06: GuardFingerprint did not change when guard key %q changed", key)
		}
	}
}

func cloneSessionState(state *driver.SessionState) *driver.SessionState {
	if state == nil {
		return nil
	}
	out := &driver.SessionState{ResumeID: state.ResumeID, DisplayID: state.DisplayID}
	if len(state.Data) > 0 {
		out.Data = make(map[string]string, len(state.Data))
		for k, v := range state.Data {
			out.Data[k] = v
		}
	}
	return out
}

func checkLiveRun(t *testing.T, d driver.Driver, c *suiteConfig) {
	t.Helper()
	if c.liveSkipReason != "" {
		t.Skip(c.liveSkipReason)
	}
	if c.livePrompt == "" {
		t.Skip("live run not configured: pass WithLiveRun to exercise the event timing contract against the real provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.liveTimeout)
	defer cancel()

	sink := NewRecordingSink()
	req := driver.Request{
		RunID:     "adaptertest-live",
		Prompt:    c.livePrompt,
		Config:    c.config,
		Streaming: true,
		Metadata:  map[string]string{"adaptertest": "v1"},
	}
	if c.workspaceCWD != "" {
		req.Workspace = driver.WorkspaceLease{ID: "adaptertest-ws", CWD: c.workspaceCWD}
	}
	resp, err := d.Run(ctx, req, sink)
	reportViolations(t, VerifyOutcome(&resp, err))
	if err != nil {
		t.Fatalf("live run failed: %v\nstderr tail: %s", err, rawStderrTail(&resp))
	}

	reportViolations(t, VerifyRunEvents(sink.Events()))
	if support, ok := d.(driver.StreamSupport); ok {
		reportViolations(t, VerifyStreamSequence(sink.Stream()))
		reportViolations(t, VerifyStreamCapability(support.StreamCapability(), sink.Stream()))
	}
	reportViolations(t, VerifyTranscriptMirror(sink.Events(), resp.Transcript))
	if resp.Output == "" {
		t.Log("note: live run returned an empty Output")
	}

	reportViolations(t, VerifyCheckpointCodec(d, &resp))
}

func checkLiveStructuredOutput(t *testing.T, d driver.Driver, desc driver.Descriptor, c *suiteConfig) {
	t.Helper()
	if c.liveSkipReason != "" {
		t.Skip(c.liveSkipReason)
	}
	if c.livePrompt == "" || !c.liveStructured {
		t.Skip("structured live probe not configured: pass WithLiveRun and WithLiveStructuredOutput")
	}
	so := desc.StructuredOutput
	if !so.JSONSchemaNative || !so.WorksWithRun {
		// SO-03: the suite never sends a mode the descriptor does not declare.
		t.Skipf("SO-03: descriptor does not declare native structured output for Run (JSONSchemaNative=%v WorksWithRun=%v)", so.JSONSchemaNative, so.WorksWithRun)
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.liveTimeout)
	defer cancel()

	sink := NewRecordingSink()
	req := driver.Request{
		RunID:  "adaptertest-live-structured",
		Prompt: `Return a JSON object with a single boolean property "ok" set to true. Reply with only the JSON.`,
		Config: c.config,
		// Streaming stays false so the probe remains inside the declared
		// matrix even when WorksWithStreaming is false (SO-03).
		Streaming: false,
		OutputSchema: &driver.OutputSchema{
			Format:     driver.OutputFormatJSONSchema,
			Mode:       driver.StructuredOutputNativeStrict,
			SchemaJSON: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
			Name:       "adaptertest_result",
		},
	}
	if c.workspaceCWD != "" {
		req.Workspace = driver.WorkspaceLease{ID: "adaptertest-ws-structured", CWD: c.workspaceCWD}
	}
	resp, err := d.Run(ctx, req, sink)
	reportViolations(t, VerifyOutcome(&resp, err))
	if err != nil {
		t.Fatalf("SO-02: native_strict structured run failed: %v\nstderr tail: %s", err, rawStderrTail(&resp))
	}
	result := resp.StructuredOutput
	if result == nil {
		t.Fatal("SO-02: native_strict run returned nil StructuredOutput; native enforcement must report the validated business value (StructuredOutput docs)")
	}
	if result.Source != driver.StructuredOutputSourceNative {
		t.Errorf("SO-02: StructuredOutput.Source = %q, want %q", result.Source, driver.StructuredOutputSourceNative)
	}
	if !result.Valid {
		t.Errorf("SO-02: StructuredOutput.Valid = false (validation errors: %v)", result.ValidationErrors)
	}
	if len(result.RawJSON) == 0 || !json.Valid(result.RawJSON) {
		t.Errorf("SO-02: StructuredOutput.RawJSON is not a valid JSON document: %q", string(result.RawJSON))
	}
}

func rawStderrTail(resp *driver.Response) string {
	if resp == nil || resp.RawStreams == nil {
		return "(no raw streams)"
	}
	stderr := strings.TrimSpace(resp.RawStreams.Stderr)
	if stderr == "" {
		return "(empty)"
	}
	const max = 2000
	if len(stderr) > max {
		stderr = stderr[len(stderr)-max:]
	}
	return stderr
}
