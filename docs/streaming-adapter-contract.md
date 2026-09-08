# Driver Streaming Contract

This document defines the live-event, final-result, and protocol-parsing contract that Driver authors must satisfy. Application hosts should read the [Streaming guide](./streaming.md); for extension authors the godoc of the [`driver`](../driver/doc.go) package and the [`adaptertest`](../adaptertest/doc.go) clauses are the final executable specification.

## 1. Layer boundaries

The application layer always faces the same `Runner`, `Stream`, `Event`, and `Result`. A Driver does not implement a second execution entry point and never touches a bridge:

```go
type Driver interface {
	Descriptor() Descriptor
	ValidateConfig(cfg any) error
	Run(ctx context.Context, req Request, sink EventSink) (Response, error)
}
```

`ValidateConfig(nil)` must not panic. For a Driver such as `mydriver.Driver(cfg)` that has already captured its construction config, nil means "validate the already captured config" and must not be interpreted as a missing config; execution and Inspect probes must observe the same construction-time config.

Division of responsibility:

- core: merges options, resolves workspace, profile, skills, MCP, runtime, and schema, coordinates the Thread, validates capability, converts SPI events into a single public typed Event stream, and forms the final `Result`.
- Driver: validates its own config, executes the provider, parses the official protocol, and from that same parse forms `StreamPayload`, `Transcript`, `Output`, `Summary`, `RawStreams`, the terminal payload, and the checkpoint.
- process helper: starts and stops the process, passes stdin, captures the complete stdout/stderr, sends raw chunks, and tees the raw data to the Driver parser; it must not guess provider semantics or checkpoints.
- bridge: only translates the public `Runner`, `Stream`, `Event`, and `Result` into external protocols such as AG-UI, SSE, and A2A; it does not call a Driver.

The `driver` package itself must not import the module root package, a concrete provider, a bridge, or an internal implementation. Third-party implementations depend only on the public `driver` SPI; a built-in Driver may localize repository-private implementation inside its own package, but its public Config and signatures must not leak private types.

## 2. Provider transport and `StreamCapability`

Live Events are an intrinsic SDK capability; whether a provider has a native fine-grained transport is a separate dimension. A Driver that can supply normalized live payloads implements the optional interface:

```go
type StreamSupport interface {
	StreamCapability() StreamCapability
}

type StreamCapability struct {
	Native       bool
	TokenLevel   bool
	Reasoning    bool
	ToolCallArgs bool
	HITL         bool
}
```

The fields must be conservative, deterministic, and stable across instances:

- `Native`: the underlying transport is a formal event protocol rather than events guessed from free text.
- `TokenLevel`: a single assistant message can produce multiple fine-grained text deltas.
- `Reasoning`: the formal protocol exposes a thinking/reasoning lifecycle.
- `ToolCallArgs`: the formal protocol exposes incremental tool arguments; otherwise the complete arguments should be placed in the tool-call opening payload.
- `HITL`: the formal protocol exposes human decision events; a truly blocking response still goes through `DecisionCapableSink`.

`StreamCapability` does not decide whether an application can call `Runner.Stream`, and it does not indicate whether a remote A2A peer supports streaming. An A2A transport may only negotiate based on the remote Agent Card.

## 3. `Request.Streaming`

`driver.Request` is a single invocation that core has already resolved. The fields most relevant to this contract are:

```go
type Request struct {
	RunID        string
	Prompt       string
	Config       any
	Session      *SessionContext
	OutputSchema *OutputSchema
	Streaming    bool
	// Plus the resolved Agent, Workspace, Runtime, Skills, MCP, Profile,
	// Policy, Instructions, Metadata, and ModelOverride.
}
```

A configured Driver that receives `Config=nil` continues to use the Config captured at construction time. Third-party implementations must not treat it as a new empty config, which would let execution, capability probes, and Inspect observe different semantics.

`Request.Streaming` selects the provider-native rich-event transport. It is set by core from the resolved invocation, `StreamCapability`, structured output, and approval compatibility; it is not determined by whether the application called `Run` or `Stream`. Both application verbs may receive `Streaming=true` or `false`.

When `Streaming=true`:

- The Driver should use its declared rich-event transport; only with `Native=true` may it additionally claim that this is a provider-native formal event protocol.
- The `StreamPayload` lifecycle in this document must be satisfied.
- Every result layer of `Response` must still be complete; live events do not replace the final response.

When `Streaming=false`:

- The Driver uses a compatible provider transport.
- It should still supply raw chunks, transcript items, and operational events through `EventSink.Emit`.
- It must not switch to a different provider protocol on its own just because the application used `Runner.Stream`.

### 3.1 Native append and active budget

`Descriptor.SystemPrompt.Append` declares a native append channel, with false
as the conservative default. `Request.AppendSystemPrompt` is one resolved exact
string, separate from Prompt, Instructions and schema prompting. Drivers must
preserve provider defaults, reject conflicting managed arguments before launch,
and include the append hash in persistent startup signatures and checkpoint
guards. Core validates invalid UTF-8/NUL and unsupported nonempty requests before
resources and layers the hash over every existing Thread compatibility dimension.
Empty append retains the old base fingerprint. Only the corresponding Driver
owns native argv/file/RPC translation; an event or helper must not interpret it.

Core owns `Policy.ActiveExecutionTimeout`; Drivers do not create a parallel
timer or approval policy. `FailureActiveExecutionTimeout` is the SPI failure
code, and unhealthy/expired work cannot produce a valid checkpoint. Ask pause
tokens are managed by the unique decision sink before queueing/callback. Parent
deadlines and lease renewal continue. Core settles the budget before atomic
persistence; Finalize must still succeed with the original context. See
[run policy](./run-policy.md#active-execution-budget).

## 4. `EventSink`

```go
type EventSink interface {
	Emit(event RunEvent) error
	EmitStream(payload StreamPayload) error
}
```

- `Emit`: operational events and transcript items as they are parsed.
- `EmitStream`: normalized text, thinking, tool, step, run, HITL, and provider drop payloads.

Both end up on the same public `Event` channel; they are not two host streams. A Driver must not retain the sink, and must not call it after `Run` returns.

Before `Run` returns, close admission to this invocation, finish every already admitted callback that can mutate its Response or use its sink, and join goroutines owned by the invocation before freezing its result. A persistent transport reader may remain alive after that handoff; later idle or next-turn output must neither mutate the old Result nor emit to its sink. Already read in-flight output belonging to the current invocation cannot be relabeled as future output to avoid this completion boundary. On context cancellation, stop the provider, unblock stdin and reader waits, and complete the existing bounded process/drain cleanup. No producer may emit to a completed invocation.

Shared process input has separate transitions: `Close` stops accepting input while a healthy writer drains accepted frames in FIFO order; terminal completion releases blocked writers. Both writer completion and process exit may publish the same terminal notification, which must be idempotent. Neither transition interprets provider payloads, failure reasons, or checkpoints.

## 5. `StreamPayload` lifecycle

Every run with `Request.Streaming=true` must:

1. Emit exactly one `StreamRunStarted` as the first frame.
2. Have intermediate frames obey their respective lifecycle and field requirements.
3. Close every opened text, reasoning, tool, and step lifecycle before the terminal frame.
4. Emit exactly one `StreamRunFinished` or `StreamRunError` as the last frame.
5. Emit no payload after the terminal frame.

The `MessageID` and `ToolCallID` of the three `StreamRun*` payloads must be empty; a run lifecycle must not masquerade as a message or tool lifecycle.

Canonical events:

| Kind | Required/key fields | Contract |
|---|---|---|
| `StreamRunStarted` | optional provider `RunID` / `ThreadID` / `TurnID` | Once per run and first; the SDK RunID is assigned separately by core |
| `StreamRunFinished` | optional `Usage` | Normal terminal frame |
| `StreamRunError` | `Error` | Failure terminal frame; `Error` must not be empty |
| `StreamStepStarted` / `StreamStepFinished` | `Name` | Paired provider step |
| `StreamTextStart` | `MessageID` | Opens an assistant message |
| `StreamTextContent` | `MessageID`, non-empty `Delta` | Only allowed inside an open message |
| `StreamTextEnd` | `MessageID` | Closes the message |
| `StreamReasoningStart` / `Content` / `End` | `MessageID`, non-empty `Delta` for content | Paired in the same way as text |
| `StreamToolCallStart` | `ToolCallID`, `Name`, optional complete `Args` | Opens a tool call |
| `StreamToolCallArgs` | `ToolCallID`, non-empty `Delta` | Emitted only when `ToolCallArgs=true` |
| `StreamToolCallEnd` | `ToolCallID`, optional `Result` | Closes the tool call |
| `StreamToolCallResult` | known `ToolCallID`, optional `Result` | Standalone completion result |
| `StreamHITLRequested` / `Resolved` | the corresponding structured envelope | Read-only audit broadcast; carries no responder |
| `StreamDropped` | gap information such as `Raw["dropped_count"]` | Loss reported by the provider itself |

The opening, content, and closing frames of one ID must be strictly ordered; different IDs may interleave. The hard negative capability contracts apply only to the corresponding event family: with `ToolCallArgs=false` a Driver must not emit `StreamToolCallArgs`, with `Reasoning=false` it must not emit reasoning events, and with `HITL=false` it must not emit HITL broadcasts. `Native=false` and `TokenLevel=false` describe the transport origin and granularity; they do not forbid a coarse text lifecycle.

An unknown formal provider event may use a custom `StreamKind` and place fields that cannot be normalized in `Raw`. Core converts it into a `Notice`, and a bridge may further degrade it into a custom event of the external protocol; events with audit value must not be silently swallowed.

### 5.1 Role

Every `StreamPayload` a Driver emits must leave `Role` at its zero value `RoleAssistant`. `RoleUser` is only allowed when a bridge or host synthesizes a human input lifecycle above the Driver; a Driver must not replay the user prompt and pass it off as its own output.

### 5.2 Sequence authority

A Driver must leave the following fields at their zero value:

- `StreamPayload.Sequence`
- `StreamPayload.Seq`
- `StreamPayload.Timestamp`
- `RunEvent.Seq`

Core serializes the receive order of the unified sink and assigns the public `EventMeta.Sequence` and time. A Driver may retain provider coordinates in fields such as `RunID`, `ThreadID`, and `TurnID`, which core places in `EventMeta.Source`; they never override the authoritative SDK ordering.

Do not pre-allocate sequences across multiple goroutines. As long as the sink is called in protocol order, core guarantees that the public channel order matches `EventMeta.Sequence`.

## 6. `RunEvent` and the transcript mirror

`EventSink.Emit` supports:

- `RunEventChunk`: `Stream` may only be `stdout` or `stderr`, and `Bytes` may use any chunk boundary.
- `RunEventItem`: `Item` must be a valid `TranscriptItem`.
- `RunEventInvocation`, `RunEventSpawn`, `RunEventRuntime`, `RunEventLifecycle`: use `Text`, `Metadata`, and `Data`.

The receive order of `RunEventItem` must equal the final `Response.Transcript` item by item and in full. A Driver must not recompute the transcript with a different heuristic at the end, and must not emit one version while returning another.

The transcript may only come from the Driver's parse of the formal provider protocol. Text, thinking, tool call/result, init, terminal result, question, and failure must use the correct `TranscriptKind` and fields; a shared helper must not scan arbitrary JSON to guess semantic items.

## 7. The `Response` contract

```go
type Response struct {
	Output           string
	Summary          string
	RawStreams       *RawStreams
	Transcript       []TranscriptItem
	Usage            *Usage
	Checkpoint       *Checkpoint
	StructuredOutput *StructuredOutput
	RuntimeServices  []RuntimeServiceReport
	Failure          *RunFailure
	ExitCode         int
	Signal           string
	TimedOut         bool
	// Plus the provider/model/metadata fields.
}
```

A single parse of the official protocol must form all of these layers together:

- `Output`: the final assistant-facing text. It must not contain raw stdout/stderr, the Summary, or terminal JSON.
- `Summary`: an optional, short summary for the host. Leave it empty when there is no formal summary; it must not fall back to the complete `Output`.
- `RawStreams`: the complete, untruncated stdout and stderr, plus the optional official terminal payload.
- `Transcript`: normalized semantic items produced by the formal parser.
- `Usage`: normalized accounting filled only from what the provider actually reported.
- `RuntimeServices`: service reports the Driver actually observed; host declarations must not be echoed back as evidence of success.
- `Failure`: a structured business failure; the terminal outcome is still returned through core's Go error path.

When Driver `Run` returns an error, any accompanying `Checkpoint.Valid=true` is
invalid. Return the available `Response` alongside the original cause: core maps
its output, Raw, terminal payload, Transcript, Usage and observed services into
`RunError.Result`, and preserves the error in `RunError.Cause`. Public execution
returns `nil, *RunError`; `Response.Failure` uses the same carrier. Do not discard
partial protocol observations just because the execution failed. Pre-Driver
failures retain their ordinary wrapping. A cleanup failure after an already
committed healthy checkpoint does not undo that commit.

Public lifecycle events are core-owned for every admitted invocation. Driver
terminal frames remain required protocol observations, and their raw payload
remains in Result; they cannot announce public success before source flush,
observer shutdown and resource cleanup. The final core RunFinished reflects the
same Result/error returned to the caller. Static pre-admission rejection has no
Driver run and retains an empty closed stream.

`StreamCapabilityInvocation` and `StreamTodoUpdated` carry validated leaf
`capability.Invocation` and `todo.Snapshot` facts. Parent scope/tool coordinates
and at most eight acyclic upstream source levels preserve provenance; raw
provider sequence values never replace core Sequence. Descriptor observation
support is truthful per transport, with zero meaning unsupported. Only formal
provider parsers may produce provider evidence; shared trackers cannot guess
capabilities from arbitrary JSON. [Streaming](./streaming.md) defines the host
observer and loss semantics.

## 8. Raw and the provider terminal

```go
type RawStreams struct {
	Stdout   string
	Stderr   string
	Terminal *TerminalPayload
}

type TerminalPayload struct {
	Event string
	JSON  json.RawMessage
}
```

Hard constraints:

- `Stdout` / `Stderr` are the complete, untruncated bytes the subprocess wrote during this run, without semantic substitution or line-by-line rewriting.
- A JSON-RPC/app-server reader must tee the raw stdout before decoding; at the end it must wait for the reader, the process wait, and the stderr collector to finish before taking the snapshot.
- `Terminal` comes only from the official provider terminal event the Driver recognized.
- `Terminal.Event` is the provider-native event/method name.
- `Terminal.JSON` retains the exact JSON value the parser recognized; it must not be synthesized from `Output`, `Summary`, `Transcript`, or arbitrary nested JSON.
- `Terminal` is nil when no official terminal event was observed.

The application `Run` and `Stream.Result()` must obtain an equivalent `Result.Raw()`. Live deltas do not substitute for the complete Raw.

## 9. Checkpoint safety

`Checkpoint.Valid=true` is only allowed when all of the following hold:

- The provider process exited successfully with `ExitCode == 0`.
- There was no signal, timeout, context cancellation, Driver error, or `Response.Failure`.
- The official parser observed an explicit successful terminal event.
- That formal protocol event provided a top-level, explicit resume/session identifier.
- `State` is non-nil and `State.ResumeID` is non-empty.
- The `SessionCodec` of the same Driver accepts the state and can round-trip it losslessly.
- The codec produces a non-empty, deterministic guard fingerprint for that state.

An init/session announcement, partial output, a nested or guessed ID, a malformed protocol, a missing terminal event, a non-zero exit, and a terminal error event are all insufficient to form a valid checkpoint.

`Descriptor.Sessions.SupportsResume=true` if and only if the Driver satisfies both construction-time contracts: it implements `SessionCodecProvider` and stably returns a non-nil, stably named codec; and it implements `SessionConfigFingerprinter` and stably returns a non-empty, cross-process deterministic fingerprint of the construction config. The latter must cover every provider-visible construction-time config value plus the codec/version contract; values that cannot be expressed stably must raise an error rather than be silently omitted. A public Thread invocation that is missing either contract is rejected with `adaptor.ErrThreadIncompatible` before acquiring the workspace/runtime/store lease or calling `Driver.Run`. The codec's nil/zero mapping, guard fingerprint, round-trip, and config fingerprint truthfulness must satisfy the `CAP-*` / `SES-*` clauses of `adaptertest`.

`ProfilePayload.Fingerprint` always identifies the exact resolved profile desired
state passed to the Driver. Resume and persistent-process guards use
`ProfilePayload.SessionFingerprint()` instead. Core may normalize only its own
ephemeral transport allocations in the session value; the Driver must still
materialize the exact current MCP/profile payload on every resumed turn.

There is no checkpoint exception for a failed run. Without a healthy checkpoint, core keeps the Thread's previous active record and does not allow a failed run to pollute resumable state.

## 10. HITL

When a Driver must block waiting for a host decision, it performs an optional interface assertion on the sink:

```go
if decisionSink, ok := sink.(driver.DecisionCapableSink); ok {
	resp, err := decisionSink.RequestDecision(ctx, driver.DecisionRequest{
		Kind:   driver.HumanDecisionPermission,
		Source: "mydriver.tool",
		Prompt: "Allow this operation?",
		Payload: map[string]any{"tool": "shell"},
	})
	// Advance or stop the provider protocol based on resp.Result or err.
}
```

Core assigns the missing request ID, time, and deadline, enforces `Policy.Approvals`, and projects an answerable request into the public `*ApprovalRequest` or hands it to the `OnApproval` callback. A Driver must not maintain a second host channel, HTTP endpoint, or retry policy of its own.

The `StreamHITLRequested` / `StreamHITLResolved` payloads a Driver emits are provider audit broadcasts that core projects into a `Notice`; they carry no responder and cannot replace `RequestDecision`. `Descriptor.RunPolicyCaps` and `StreamCapability.HITL` must truthfully express decision capability and protocol visibility respectively.

## 11. Backpressure and cancellation

A Driver does not choose the host backpressure policy. The default broker may drop high-frequency deltas, but in a normal run that is not cancelled, approvals, lifecycle frames, terminal frames, transcript items, tool results, and drop reports are reliable events; therefore even in the default mode a sink call may wait for consumer space. An explicit cancellation enters abort teardown and makes no promise to keep delivering critical events that have not yet entered the channel.

Under `WithBlockingEvents`, every event may apply backpressure to the Driver. A Driver must keep process I/O, parser callbacks, and event emission sensitive to context cancellation, so that the reader, the process wait, and the sink never form a closed waiting loop.

The rules are simple:

- Check `ctx.Done()` continuously.
- Stop the provider and the stdin writer when the context ends.
- Wait for every I/O goroutine to exit before returning.
- Do not close the sink; the channel lifecycle is owned exclusively by core.
- Do not ignore helper/sink errors that may signal cancellation.

## Formal observations and runtime publishers

The [provider observation matrix](./streaming.md#provider-observation-support)
describes actual transports. Drivers alone recognize official fields and map
unique resolved catalog keys. Complete arguments and real deltas are not replayed
twice; tool description end is distinct from result-confirmed completion.
Todo snapshots require confirmed protocol evidence, including explicit clear,
and synthetic display IDs cannot stand in for provider task identity. These
projections coexist with original tool events and Transcript; Todo is not a
PlanReview approval. Capability observation is best effort, not an authorization
decision or a complete audit/billing record. Missing facts do not prove non-use.

Codex NativeInputAccepted means the formal typed input was accepted, not that
the resource executed. Its current thread/turn and terminal fences remain;
formally valid historical same-thread usage may be audit-only, without updating
current Usage or checkpoint. Required usage field presence distinguishes missing
from explicit zero. Known stdout EOF must enter bounded drain/process settlement
before a resident turn claims health, preserving observed Wait errors and Raw;
a connected resident turn does not wait for process exit.

Delegation is a public Runner consumer. Its bound publisher enters the same sink
and observer path before the lossy component bus, carries safe typed facts once,
preserves source provenance and reports unrepresentable relay identity/depth.
Only a successful binding establishes historical proof for transparent Merge.
No helper, bridge or observer parses provider JSON or creates a second stream.

### Conformance clauses for observations

The executable clauses in [adaptertest godoc](../adaptertest/doc.go) apply to
the resolved provider transport, independently of the consumer's Run/Stream verb:

| Clause | Required behavior |
|---|---|
| DRV-03 | Descriptor calls return independent mutable HITL matrices. |
| EVT-09 / EVT-10 | Every StreamPayload leaves Role, Sequence, Seq and Timestamp at their required zero values, including observation-only and batch payloads. |
| EVT-14 | Tool identity includes ScopeID; explicit parents stay stable, complete Args are not followed by ArgsDelta, and results are unique. |
| OBS-01 / OBS-02 | Facts respect the actual transport declaration and carry exactly their typed semantic payload, without unsafe generic fields. |
| OBS-03 | Capability values follow the closed evidence, encoding, size, time and duration rules; a parent cannot be the same scoped invocation. |
| OBS-04 | Each scoped capability starts and ends once, with stable identity and no open start at provider termination. |
| OBS-05 / OBS-06 | Todo is a confirmed full ordered snapshot; non-nil empty clears, IDs are unique, and revisions start at one and advance only for changes. |

Equal IDs in different scopes are distinct. Observation-only transports need
no invented rich run frames. These checks cannot prove provider provenance,
catalog attribution or model behavior; formal protocol fixtures and explicitly
gated real-provider tests supply that evidence. Core still owns the final public
outcome after resource cleanup. Existing MUST/SHOULD and SessionCodec laws remain.

## 12. `adaptertest` acceptance

Every Driver must run the final conformance suite directly:

```go
func TestMyDriverConformance(t *testing.T) {
	adaptertest.TestDriver(t, func() driver.Driver {
		return mydriver.Driver(mydriver.Config{Model: "m-1"})
	},
		adaptertest.WithSessionState(&driver.SessionState{
			ResumeID: "session-1",
			Data: map[string]string{"cwd": "/repo"},
		}),
		adaptertest.WithSessionKeys("cwd"),
		adaptertest.WithGuardKeys("cwd"),
	)
}
```

Every hermetic clause that applies to that Descriptor, to the implemented optional interfaces, and to explicit opt-ins is executed, covering Driver/config, capability truthfulness, the structured-output matrix, SessionCodec, and SessionConfigFingerprinter; optional capabilities that do not apply are explicitly skipped. Real provider execution is enabled explicitly through `WithLiveRun` and is protected by the provider package's explicit live build tag and environment gate; enabled required probes fail when CLI or evidence is missing, so ordinary CI must not produce paid calls.

The clause groups directly related to this contract are:

- `CAP-10`: `StreamCapability` determinism.
- `EVT-*`: run/text/reasoning/tool/step/HITL lifecycle, Role, zero sequence, terminal-last, and capability negatives.
- `RUN-*`: raw chunks, transcript items, core-owned `RunEvent.Seq`, and the transcript mirror.
- `TRN-*`: field validity for each `TranscriptKind`.
- `RSP-*`: checkpoint cleanliness, the Failure invariant, Output layering, codec round-trip, and official terminal JSON.
- `SES-09`: exact profile materialization remains separate from the session compatibility guard.

Beyond the suite, a Driver must itself cover, for each formal protocol path: success, non-zero exit, malformed protocol, missing checkpoint, missing terminal event, cancellation, and provider-specific parser lifecycles. Different transports such as the Codex CLI and app-server must each have their own contract tests; one path must not vouch for the other.

The release gate includes at least:

```text
go test -count=1 ./adaptertest ./yourdriver/...
go vet ./adaptertest ./yourdriver/...
```

Linux CI must additionally run race; key parsers and the archive parser run fuzz according to the repository convention.
