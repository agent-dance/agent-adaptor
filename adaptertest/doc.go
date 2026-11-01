// Package adaptertest is the conformance suite for the driver SPI. It verifies
// implementations of [driver.Driver] directly and has no dependency on the
// root package's consumer API.
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
//			adaptertest.WithMessageFixture("streaming", myMessageFixture),
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
//	        a non-nil, stable codec; resume-capable Drivers also implement
//	        SessionConfigFingerprinter with a stable, non-empty result.
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
//	CAP-11  Process.Persistent implies ProcessLifecycleDriver, so Agent.Close
//	        can reap every process owned by the configured Driver.
//
// Structured output (driver/driver.go StructuredOutputCapability,
// driver/output.go; engine resolution in internal/engine/structured.go):
//
//	SO-01  WorksWith* flags require a declared JSONSchema* mechanism, and a
//	       declared mechanism requires WorksWithRun for v2's one Send execution
//	       pipeline. Core selects native enforcement first and Prompt
//	       validation as the fallback. Provider streaming and effective HITL
//	       Ask additionally require WorksWithStreaming and WorksWithHITL.
//	SO-02  (live, opt-in) a native structured run yields StructuredOutput with
//	       Source=native, Valid=true and parseable RawJSON.
//	SO-03  Suite guarantee: no probe requests a mechanism or transport shape
//	       the descriptor does not declare.
//
// Message delivery (driver/messages.go; hermetic WithMessageFixture):
//
// MSG-01  Boundaries, ContinueMode and size units are valid; Interrupt implies
//
//	immediate; persisted guidance implies resume support.
//
// MSG-02  The resolved capability is deterministic and contained in Descriptor;
//
//	every declared boundary/Interrupt is exercised by a request fixture;
//	no claim at a resolved-away boundary. Nil Request.Messages works.
//
// MSG-03  Provider-confirmed windows precede claims and are closed at Run return.
//
//	Turn-end claims may follow a closed active-turn window. An initial
//	Window(false) is permitted; subsequent window changes alternate.
//
// MSG-04  Each claim is acked exactly once before Run returns, on success,
//
//	delivery-write failure, process exit, cancellation and timeout; no
//	unknown Ack, double claim or use of a finished port.
//
// MSG-05  Delivered requires verbatim provider input and a concrete mechanism;
//
//	failed requires Err. Requeue of possibly received input requires
//	MaybeDelivered; only proven rejection/non-receipt permits plain retry.
//
// MSG-06  ConfirmedDelivery requires an official echo; the missing-echo script
//
//	must not acknowledge delivery merely because an HTTP/pipe write worked.
//
// MSG-07  Exactly one TurnCompleted per observed provider turn, including in-run
//
//	continuations, contiguous zero-based Index, nonzero CompletedAt, and
//	preserved text, observed Usage (including nil vs zero), Interrupted.
//
// MSG-08  ContinueInRun claims turn_end and starts subsequent provider turns in
//
//	the same Run. ContinueResume leaves items to core; the kit models a
//	core dispatch with the healthy checkpoint and verbatim pending prompt,
//	and checks the fake actually observed resume/reused conversation.
//
// MSG-09  Framed admission uses bytes (including the empty-unit default) or
//
//	UTF-16 correctly. Exact-limit input fits; limit+1 is deferred. Batches
//	of individually fitting inputs are settled without truncation or false
//	delivery; an aggregate transport limit may defer later inputs.
//
// MSG-10  PersistsDelivered requires independently observed retained guidance
//
//	after checkpoint resume, for both boundary and core-resumed inputs.
//
// A message fixture supplies a configured real Driver, a resolved request shape,
// and observations from its scripted fake CLI. The kit supplies the port and
// chooses all scenarios implied by that shape's resolved capability. See
// NewReferenceMessageFixture and ScriptedMessagePort. Supply multiple fixtures
// for transport/workspace-dependent resolution; fixtures collectively cover the
// static declaration. Only MSG-01 runs without a fixture; execution is explicitly
// skipped, so a fixture-free suite is not message-conformance evidence. These
// hermetic clauses do not verify actual CLI versions or replace gated live runs.
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
//	SES-09  Resume and persistent-process guards use
//	        ProfilePayload.SessionFingerprint(); exact Fingerprint and payload
//	        remain authoritative for current-invocation materialization.
//
// Stream event timing (live; driver/events.go StreamKind, StreamPayload
// field-usage table, Role; driver/driver.go EventSink):
//
//	EVT-01  run.started is emitted exactly once and before every other payload,
//	        including vendor extensions and provider-side drop reports.
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
//	        outcome (Driver.Run error nil, exit 0, no signal/timeout/Failure).
//	RSP-02  Failure.HumanDecision is non-nil exactly when Code is
//	        decision_rejected or decision_timeout.
//	RSP-03  Output is never the raw protocol-shaped stdout dump.
//	RSP-04  a valid checkpoint round-trips through the session codec with
//	        its ResumeID and a non-empty guard fingerprint.
//	RSP-05  when a provider exposes terminal JSON, RawStreams.Terminal carries
//	        its non-empty official event name and valid exact JSON payload.
//
// These clauses are normative. The driver package states the lifecycle,
// codec-empty mapping, core-owned sequence, transcript mirror, resume-codec,
// checkpoint, structured-output transport and HumanDecision requirements as
// MUST rules; this suite does not offer lenient/advisory escape hatches.
package adaptertest
