// Package adaptertest (import path .../adaptertest/v1) is the conformance
// suite for the v1 driver SPI: it verifies implementations of
// [driver.Driver] directly, with no dependency on the legacy root-package
// binding surface (unlike the parent adaptertest package, which exercises
// the v0 Subject/Bind flow).
//
// Usage follows the fstest.TestFS / nettest.TestConn idiom:
//
//	func TestMyDriverConformance(t *testing.T) {
//		adaptertest.TestDriver(t, func() driver.Driver {
//			return mydriver.Driver(mydriver.Config{Model: "m-1"})
//		},
//			adaptertest.WithConfig(mydriver.Config{Model: "m-1"}),
//			adaptertest.WithSessionState(&driver.SessionState{...}),
//			adaptertest.WithGuardKeys("cwd"),
//		)
//	}
//
// Probes for the optional capability interfaces skip when an interface is
// not implemented, and the suite never exercises a capability the
// Descriptor does not declare. NewReferenceDriver returns a fully
// compliant in-memory implementation that doubles as the suite's
// self-proof and as a template for driver authors.
//
// # Contract clause catalogue
//
// Every failure message carries one of the numbered clauses below. The
// authority for each clause is the godoc in package driver (file noted per
// group); clauses marked (opt-in) run only when the corresponding Option
// is passed, and clauses marked (live) require WithLiveRun.
//
// Core driver and config (driver/driver.go: Driver, Descriptor):
//
//	DRV-01  Descriptor().Type and DisplayName are non-empty.
//	DRV-02  Descriptor() is deterministic across calls and across factory
//	        instances (the descriptor is a static declaration).
//	CFG-01  ValidateConfig(nil) must not panic.
//	CFG-02  ValidateConfig accepts the suite-supplied config.
//	CFG-03  (opt-in, ExpectRejectForeignConfig) ValidateConfig rejects a
//	        config value of an unknown type.
//
// Capability-declaration truthfulness (driver/driver.go capability
// interfaces; driver/environment.go report types):
//
//	CAP-01  Sessions.SupportsResume is true iff SessionCodecProvider returns
//	        a non-nil, stable codec.
//	CAP-02  Skills.Supported implies SkillSupport and a declared Mode of
//	        ephemeral|persistent; unsupported skills must not declare a mode.
//	CAP-03  CheckEnvironment succeeds hermetically; DriverType matches the
//	        descriptor; Status is pass|warn|fail; pass implies Healthy;
//	        checks carry stable Codes.
//	CAP-04  ListModels succeeds hermetically; every ModelInfo.ID non-empty.
//	CAP-05  DetectModel succeeds hermetically; a non-nil result carries a
//	        non-empty Model (and matches WithExpectedDetectedModel).
//	CAP-06  GetProfile: DriverType matches; Supported implies non-empty Dir
//	        and Source.
//	CAP-07  GetQuota succeeds hermetically; DriverType matches; window
//	        labels non-empty.
//	CAP-08  ConfigSchema succeeds; field names non-empty and unique;
//	        contains WithRequiredConfigFields.
//	CAP-09  SkillSupport echo laws on the empty catalogue: Supported=true,
//	        DriverType matches, valid Mode, Selected mirrors the (empty)
//	        selection. SyncSkills half is opt-in (WithSyncSkillsProbe).
//	CAP-10  StreamCapability() is deterministic.
//
// Structured output (driver/driver.go StructuredOutputCapability,
// driver/output.go; engine resolution in internal/engine/structured.go):
//
//	SO-01  WorksWith* flags require a declared JSONSchema* mechanism, and a
//	       declared mechanism requires WorksWithRun for v1's one execution
//	       pipeline. WorksWithStreaming refers to provider transport only.
//	SO-02  (live, opt-in) a native_strict Run yields StructuredOutput with
//	       Source=native, Valid=true and parseable RawJSON.
//	SO-03  Suite guarantee: no probe ever sends a mode or transport shape
//	       the descriptor does not declare.
//
// Session codec (driver/session_codec.go; driver/run.go SessionState):
//
//	SES-01  Name() is non-empty and stable.
//	SES-02  ToParams(nil) is the zero SessionParams; FromParams(zero) is nil.
//	SES-03  ToParams preserves ResumeID and all Data entries; DisplayID is
//	        non-empty for a resumable state.
//	SES-04  params -> FromParams -> ToParams is lossless (DeepEqual).
//	SES-05  GuardFingerprint is non-empty and deterministic.
//	SES-06  (opt-in, WithGuardKeys) mutating a guard value changes the
//	        fingerprint (resume-guard doctrine in the SessionCodec godoc).
//	SES-07  nil/zero codec inputs never panic.
//	SES-08  (opt-in, WithSessionKeys) required keys survive the round-trip.
//
// Stream event timing (live; driver/events.go StreamKind, StreamPayload
// field-usage table, Role; driver/driver.go EventSink):
//
//	EVT-01  run.started is emitted exactly once and before every other
//	        normalized payload.
//	EVT-02  exactly one run.finished or run.error is emitted, it is last,
//	        and run.error carries Error.
//	EVT-03  text lifecycles: MessageID required, opened once by text.start,
//	        content/end only while open.
//	EVT-04  text.content carries a non-empty Delta.
//	EVT-05  tool_call lifecycles: start requires ToolCallID+Name; args
//	        require an open lifecycle and non-empty Delta; end/result
//	        require a known ToolCallID.
//	EVT-06  reasoning lifecycles mirror EVT-03/EVT-04.
//	EVT-07  step.started / step.finished require Name.
//	EVT-08  hitl.requested / hitl.resolved carry their decision envelopes.
//	EVT-09  Role is left at the zero value on every driver-emitted payload.
//	EVT-10  Sequence, Seq, and Timestamp are left zero by drivers (the SDK
//	        backfills them in EmitStream).
//	EVT-11  every opened lifecycle is closed before run.finished.
//	EVT-12  StreamCapability negatives hold: no tool_call.args when
//	        ToolCallArgs=false, no reasoning.* when Reasoning=false, no
//	        hitl.* when HITL=false.
//	EVT-13  run.* frames leave MessageID and ToolCallID empty.
//
// Vendor-specific StreamKinds and RunEventTypes outside the declared enums
// are tolerated for field validation, but no payload may follow a terminal.
//
// RunEvent envelope and transcript (live; driver/events.go RunEvent,
// TranscriptItem):
//
//	RUN-01  chunk events carry Stream "stdout"|"stderr".
//	RUN-02  item events carry a non-nil Item obeying the TRN rules.
//	RUN-03  drivers leave RunEvent.Seq zero (SDK-assigned).
//	RUN-04  the RunEventItem sequence exactly mirrors Response.Transcript.
//	TRN-01  text-bearing kinds (assistant/thinking/user/stdout/stderr/
//	        system/summary/question/failure) require Text.
//	TRN-02  tool_call requires ToolName.
//	TRN-03  tool_result requires ToolUseID.
//	TRN-04  Delta is allowed on assistant and thinking only.
//
// Response invariants (live; driver/run.go):
//
//	RSP-01  Checkpoint.Valid=true requires State with a ResumeID and a clean
//	        outcome (exit 0, no signal/timeout/Failure).
//	RSP-02  Failure.HumanDecision is non-nil exactly when Code is
//	        decision_rejected or decision_timeout.
//	RSP-03  Question carries a Prompt.
//	RSP-04  Output is never the raw protocol-shaped stdout dump.
//	RSP-05  a valid checkpoint round-trips through the session codec with
//	        its ResumeID and a non-empty guard fingerprint.
//
// These clauses are normative. The driver package states the lifecycle,
// codec-empty mapping, core-owned sequence, transcript mirror, resume-codec,
// checkpoint, structured-output transport and HumanDecision requirements as
// MUST rules; this suite does not offer lenient/advisory escape hatches.
package adaptertest
