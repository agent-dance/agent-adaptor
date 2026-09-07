# A2A bridge, client, and delegation

`agent-adaptor` keeps A2A at the host integration boundary:

- `bridges/a2a` publishes any `adaptor.Runner` as an A2A agent.
- `clients/a2a` calls remote A2A agents through stable, protocol-shaped DTOs.
- `hosttools/a2adelegation` optionally gives a leader Agent a curated Local/Remote delegation service.

Core execution remains protocol-independent. There is no A2A Driver, automatic remote-agent routing, built-in production server, or A2A-specific execution entry point.

## Dependency choice

The bridge and client use the official `github.com/a2aproject/a2a-go/v2` SDK for Agent Card conversion, JSON-RPC/SSE transport, task events, protocol errors, and request handling.

- Reliability: protocol parsing and transport behavior stay with the official implementation instead of a parallel handwritten stack.
- Maintainability: the dependency has upstream documentation, versioning, issue tracking, and protocol evolution.
- Localization: only `bridges/a2a` and `clients/a2a` import the A2A SDK. Core, Drivers, and unrelated bridges do not.

## Publishing a Runner

`a2a.NewServer` accepts the same `adaptor.Runner` implemented by `*adaptor.Agent`, `*adaptor.Thread`, and host decorators. The bridge always executes through `Runner.Stream`, consumes the one typed `Event` channel, and obtains the terminal `Result` from `Stream.Result`. It does not dispatch a Driver directly or reproduce option merging, Thread coordination, error policy, or result construction.

```go
agent := adaptor.New(
	configuredDriver,
	adaptor.WithThreadStore(memory.NewStore()),
)

server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "1.0.0",
		URL:         "https://host.example/a2a",
		Skills: []bridgea2a.Skill{{
			ID:          "coding",
			Name:        "Coding",
			Description: "Implement and review repository changes",
			Tags:        []string{"coding", "repository"},
		}},
	},
	Session: bridgea2a.ThreadByContextID(),
	Options: []adaptor.CallOption{
		adaptor.WithPolicy(nonInteractivePolicy),
	},
	TaskLifecycle: bridgea2a.TaskLifecycleOptions{
		Ephemeral: &bridgea2a.EphemeralTaskStoreOptions{
			MaxTasks: 512,
			TTL:      2 * time.Hour,
		},
	},
})

mux := http.NewServeMux()
mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

Hosts own route layout, authentication, authorization, TLS, tenancy, rate limiting, observability, and durable task storage. `Server` only supplies mountable handlers.

### Session binding

`ServerOptions.Session` has two final modes:

- `a2a.Stateless()` executes each request on the configured Runner without cross-request memory. It is the default.
- `a2a.ThreadByContextID()` maps a non-empty A2A `contextID` to a collision-free adaptor Thread key. Follow-up requests with the same `contextID` continue the same conversation.

`ThreadByContextID` requires the configured Runner to be an `*adaptor.Agent` with `adaptor.WithThreadStore(...)`; only an Agent can create Threads. A missing `contextID` runs stateless. Passing an already selected `*adaptor.Thread` is valid with `Stateless`, because the bridge simply invokes that Runner.

### Prompt and call options

The default prompt is the last non-blank text part of the inbound A2A message. Set `ServerOptions.Prompt` to a `PromptBuilder` when the host needs domain-specific projection of data, files, or multiple message parts. `ServerOptions.Options` supplies the shared call options; the builder returns the prompt plus request-specific `[]adaptor.CallOption`, which are applied afterward.

Both sources remain call-scoped. They do not create a second set of Agent defaults.

### Event mapping and the intentional wire schema

The bridge maps adaptor events to A2A task updates in one order:

1. submitted and working task status;
2. one working `TaskStatusUpdateEvent` per exposed adaptor `Event`;
3. exactly one completed, failed, or canceled terminal outcome;
4. terminal result artifacts when allowed by the exposure policy.

Intermediate typed events use DataParts with schema `adapter.stream.v1`. The exported names `AdapterStreamSchemaV1`, `AdapterStreamEnvelopeV1`, `AdapterStreamEventV1`, and `DecodeAdapterEventV1` intentionally keep the `V1` suffix: they version an interoperable wire format, not a temporary Go API. Encoding remains an internal bridge responsibility. The server advertises `urn:agent-adaptor:stream:v1` in its Agent Card, and each DataPart is self-describing.

The wire envelope preserves adaptor `EventMeta` plus provider source coordinates, complete tool snapshots, approval fields, and detailed dropped-event markers. Unknown DataPart schemas remain protocol data; the bridge does not guess provider semantics.

### Capability, Todo and parent projection

```go
server := a2a.NewServer(agent, a2a.ServerOptions{
    AgentCard: card,
    Exposure: a2a.ExposurePolicy{
        IncludeCapabilityInvocations: true,
        IncludeTodos: true,
    },
})
```

Both new switches default to false and are independent of tools, HITL,
Transcript and diagnostics. Opt-out omits facts without revealing kind/key/count
through a notice. Capability includes only closed identifiers, operation, phase,
evidence, source enum, time/duration and error code, never args/result/Raw,
credentials, prompt or error bodies. Todo is explicit content exposure: ordered
Items preserve real/SyntheticID and status with existing inline-secret filtering;
`items: []` clears the scope, while null/missing Items is invalid.

Source/Upstream coordinates additionally require Diagnostics.IncludeMetadata;
they do not replace authoritative Meta.RunID/Sequence/Time. Tool events retain
ScopeID/ParentScopeID/ParentToolCallID. Both A2A request modes use the existing
Runner.Stream and append a private observation-demand attachment after host
options. It adds no Store, Observer, event source or second provider policy.

`adapter.stream.v1` keeps its version/URI and adds optional capability/todo/parent
and Source fields. New kinds have strict closed shapes, nonzero valid Meta,
consistent optional flat mirrors, safe JSON integer coordinates, explicit UTF-8
field limits and at most eight acyclic source nodes. Same-scope invocation
self-parent is invalid; the same raw ID in another scope can be a valid parent.
Occurrence timestamps decode to the equivalent UTC instant. Unknown duration
and observed zero stay distinct. Never truncate a Todo and call it complete.

The entire A2A envelope is at most 65536 bytes. RawMessage decoding validates
original bytes for new kinds: duplicates (including a foreign schema followed
by this schema), trailing values, invalid UTF-8 and unpaired surrogates fail.
Map/DTO inputs validate structure before encoding, but cannot reconstruct
duplicate keys already erased by an upstream decoder. Existing legacy args/raw
large values and parseable zero timestamps retain their established compatibility;
new parent/source protection is validated independently. Unknown kind in this
schema yields matched=true with an error and no partial Event.

Opaque ThreadKey and its flat ThreadID mirror preserve all legal UTF-8,
including JSON-escaped LF/tab, without trimming or a 2048-byte key limit. Existing
envelope coordinate length is bounded by the whole envelope. The explicitly
limited invocation/tool/scope/parent-scope/delegation IDs retain 2048-byte limits;
Source coordinate character rules remain unchanged.

Invalid/oversized opted-in content becomes stream.dropped with the original
Meta and no new sequence, provided that Meta itself is safely encodable. Its
Raw is only dropped_count=1, source=a2a, a closed reason and validated event_kind
(or unknown); no original payload/cause leaks. If even the original Meta cannot
fit safely, the same executor cancels and drains the Stream, reads its actual
Result/RunError.Result, emits permitted partial artifacts and returns an
observable infrastructure error. It neither fabricates coordinates nor truncates
the key/source chain. Old binary consumers must explicitly handle unsupported
new kinds. Missing facts never prove no invocation or complete audit/billing.

### Stable failure classification

The failure verdict comes only from the same drained Stream.Result error.
Known failure controls live at **Text Part.Metadata["agentadaptor.failure"]**,
not a DataPart, Task metadata or a new adapter.stream.v1 event kind:

| Primary code | A2A state | Safe status text |
|---|---|---|
| active_execution_timeout | failed | active execution budget exhausted |
| approval_denied | failed | approval denied |
| approval_timeout | failed | approval timed out |
| cancelled | canceled | task cancelled |
| deadline_exceeded | failed | execution deadline exceeded |
| agent_error | failed | agent run failed |
| policy_violation | failed | execution policy violated |
| infrastructure_error | failed | execution infrastructure failed |

RunError.Reason always takes precedence, even when empty or unknown. An unknown
carrier uses safe generic failure text without promoting a control object.
Provider error bodies and Cause.Error never become status text. Existing explicit
IncludeMetadata may add a sanitized metadata child object; it cannot replace
the top-level code or limit_ms. Translator and ResultBuilder failures keep their
own bridge error path.

Only bare errors can reuse a terminal classification from the same stream: its
fixed RunID and terminal Meta.RunID must be nonempty and equal; exactly one typed
RunFinished must be the last event, Failed=true, with one of these eight reasons,
followed by full channel closure. Provider RunFinished.RunID is not the envelope
identity. A duplicate (including a nil pointer), trailing event, wrong identity,
unknown reason or incomplete drain invalidates the hint and keeps the old bare
error fallback. A nil Result error never becomes failure because of an event.
This also covers cancellation and translation-error drains without another
execution or error channel. For example, a pre-Driver parent cancellation may
retain a typed parent budget cause while core's terminal reason is cancelled;
the projection is cancelled, not a new local active-budget exhaustion.

The only other control is optional limit_ms for primary active_execution_timeout:

```json
{"code":"active_execution_timeout","limit_ms":100}
```

It comes from the first typed timeout in carrier.Cause, provided its Limit is
positive; without a carrier the same check uses the original bare error. Outer joined parent budgets
and arbitrary Details cannot supply the carrier's limit. Missing/invalid first
typed limits are not replaced with a later parent's value. Encoding rounds up
using division/remainder: 1ns becomes 1ms; the maximum is 9223372036855ms.

Delegation accepts only finite positive integral values in that range, applying
the same mathematical domain to json.Number, int64 and safe float64 values.
Integral decimal/exponent forms such as 100.0 and 1e2 are valid; strings, booleans,
nonzero fractions, out-of-range values and extra control fields are not. Unknown
codes, conflicting controls or code/state mismatch keep a remote failure with
a safe failure_payload_invalid diagnostic. An absent limit preserves the active
category without inventing a typed duration. Conversion to nanoseconds saturates
before multiplication; it does not change the receiver's local budget.

Allowed partial Result artifacts precede terminal status for cancellation and
budget failures too. Summary remains the default; Raw, Transcript, Usage and
provider terminal payload retain their independent exposure switches. Unknown
Usage remains absent, while observed zero is retained.

### Result and exposure

Successful `Stream.Result()` values produce the completed status and terminal artifacts. For `*adaptor.RunError`, the bridge first reads the primary `Reason`: explicit cancellation maps to canceled; approval denial/timeout and other failures map to failed even when their cause also matches a context error. Both retain the partial Result allowed by ExposurePolicy. Bare errors use the qualified same-stream terminal hint described above, otherwise the existing context/error fallback. The bridge never treats a non-nil execution error as success.

The default `agent-adaptor-result` artifact contains only the safe summary. `ExposurePolicy` must explicitly opt in to reasoning, tool calls, HITL, capability facts, Todo content, metadata, usage, provider terminal payload, transcript, or raw streams. Enabled diagnostics are sanitized before leaving the bridge.

`ServerOptions.ResultBuilder` may append or replace terminal artifacts and override completed status text. It runs only after successful adaptor execution and cannot rewrite already emitted intermediate events.

Task retention is explicit. With no custom `TaskLifecycle.Store`, the bridge uses a bounded in-memory store (default 256 tasks, one-hour TTL). Configure `TaskLifecycle.Ephemeral` to change those bounds or inject an upstream `taskstore.Store` for durable or cross-process retention.

Capability advertisement is strict:

- `Capabilities.Streaming` is tri-state; the zero value enables A2A streaming, while `CapabilityDisabled` advertises `false`.
- push notifications require both card advertisement and `ServerOptions.PushNotifications` with a config store and sender.
- an extended Agent Card requires both card advertisement and `ServerOptions.ExtendedAgentCard` with a static card or provider.

Construction-time mismatches panic because they are host programming errors, not per-run failures.

### Streaming continuation and recovery

A full Task in a stream is persisted context: restore identity and artifacts,
but await a current live status/message or an explicitly marked GetTask recovery.
An old input-required/completed/failed Task cannot replay its questionnaire or
end a new continuation. Snapshot-only EOF yields `stream_interrupted` in
delegation and triggers bounded cancellation of the known remote task. Explicit
Send/GetTask polling continues to use the complete Task as its result. Local
loopback terminal events carry a live Status alongside the Task.

Historical replay cannot overwrite artifacts updated live. Explicit GetTask
recovery adopts a proven complete extension and preserves live content against
a lagging prefix. Incomparable views use the complete query and report
`subagent.stream.dropped` with `reason: artifact_recovery_conflict` and
`resolution: recovered_snapshot`; diagnostics contain only fixed labels and
the typed artifact ID, never payload or URL. Previously delivered live events
remain available. Adjacent Text chunks compare as one only when their other
attributes match; structured/file/metadata and mixed Parts retain exact
boundaries. Conflicting views are never concatenated into invented content.

## Calling a remote A2A agent

`clients/a2a` intentionally returns A2A tasks, messages, artifacts, and events. It does not pretend that a remote protocol task has local CLI stdout/stderr or an adaptor `Result`.

```go
client := clienta2a.New(clienta2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})
defer client.Close()

card, err := client.AgentCard(ctx)
if err != nil {
	return err
}

task, err := client.Send(ctx, clienta2a.SendRequest{
	Message: clienta2a.Message{
		Role: "user",
		Parts: []clienta2a.Part{{
			Kind:      clienta2a.PartText,
			Text:      "Review this change",
			MediaType: "text/plain",
		}},
	},
	AcceptedOutputModes: card.DefaultOutputModes,
})
if err != nil {
	return err
}
_ = task
```

`SendStream` and `Subscribe` return ordered protocol events. A message, final task/status, or `TASK_STATE_INPUT_REQUIRED` completes the stream. Duplicate or late events after the first terminal event are ignored. If transport fails after a task ID is known, the client attempts one `GetTask` recovery and marks a recovered terminal event with `RecoveredState`.

`GetTaskRequest` and `CancelTaskRequest` carry tenant and operation metadata:

```go
task, err := client.GetTask(ctx, clienta2a.GetTaskRequest{
	TaskID: "task-123",
	Tenant: "tenant-a",
})

_, err = client.CancelTask(ctx, clienta2a.CancelTaskRequest{
	TaskID: "task-123",
	Tenant: "tenant-a",
	Metadata: map[string]any{
		"reason": "parent_cancelled",
	},
})
```

Outbound DataPart and metadata values must encode as non-null JSON. Integer-valued numbers must remain within the interoperable IEEE-754 safe range; encode larger IDs and counters as strings. Invalid values fail before sending, and unsafe inbound DataParts return protocol errors rather than silently rounding.

Credentials are origin-pinned. Auth is sent to the Agent Card origin by default; cross-origin interfaces require `TrustedAuthOrigins`, and redirects to untrusted origins lose authorization headers. `SubscribeRequest.Since` is rejected because A2A 1.0 has no cursor replay field.

See [`examples/a2a-server`](../examples/a2a-server) for an end-to-end server and client using the final API.

## Curated Local/Remote delegation

`hosttools/a2adelegation` is an optional host component for a leader Agent. A `Service` combines:

- a host-curated registry of Local and Remote targets;
- the authenticated per-run `delegate_to_agent` MCP sidecar;
- A2A transport and event mapping;
- ordered delegation events and final result recording;
- runtime-service attachment and teardown.

Local targets execute any `adaptor.Runner` in-process. Remote targets use `clients/a2a`. Both paths pass through the same A2A-shaped mapper and emit the same `DelegationEvent` vocabulary.

```go
team, err := a2adelegation.NewService(a2adelegation.Config{
	Agents: []a2adelegation.AgentRef{
		a2adelegation.LocalNamed("plan", "Codex Planner", planner, a2adelegation.Policy{}),
		a2adelegation.Remote("review", reviewCardURL, a2adelegation.Policy{
			MaxTimeout: 2 * time.Minute,
		}),
	},
	ToolTimeout: 3 * time.Minute,
})
if err != nil {
	return err
}
defer team.Close()

leader := adaptor.New(leaderDriver, team.Option())
stream := leader.Stream(ctx, "coordinate the change")
for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		_ = event.Text
	case adaptor.SubagentUpdate:
		_ = event.Agent
	}
}
result, err := stream.Result()
```

Use `Local` when the registry key is also the only display label needed. Use
`LocalNamed` to keep the model-facing key stable while carrying a separate
human-facing name in `DelegationEvent.AgentName`; visual hosts can use names
such as `Claude Code Implementer` to show the provider base independently from
the workflow role.

`team.Option()` is a `SharedOption`: pass it to `adaptor.New` for every run or to one `Run`/`Stream` call for a single invocation. It is equivalent to the generic extension point:

```go
leader := adaptor.New(leaderDriver, adaptor.WithRunServices(team))
```

`Service` implements `adaptor.RunServiceProvider`. For each run it publishes a typed `ServiceRef.MCP` declaration, carries the bearer token only through `SecretEnv`, binds a publisher for SubagentUpdate and typed capability/todo facts in the leader's existing Event channel, and tears the sidecar down during normal run cleanup. There is no separate subagent bus in the Runner API, and MCP declarations are never inferred from stringly metadata.

The leader sees only registry keys, objectives, optional input, and bounded constraints. It never receives endpoint URLs or credentials. Unknown tool fields and attempts to provide `endpoint_url` are rejected.

`Service.Result(runID, key)` returns the latest result for an Agent key. `Service.Results(runID)` is the latest-by-key map. `Service.Delegations(runID)` preserves every delegation in `DelegationStarted` acceptance order, including repeated calls to the same Agent. Recorded results remain readable after per-run sidecar teardown and after `Service.Close`.

For lower-level integrations, `Registry`, `Delegator`, `EventBus`, and `MCPServer` remain independently usable. `StatusPartDecoder` can decode a host-owned A2A Status DataPart schema. The built-in `adapter.stream.v1` decoder preserves typed capability/todo and parent/source coordinates when permitted by ExposurePolicy. Service.AttachRun binds one publisher with Events=nil. Cloned facts reach the
core sink and bounded observers before the lossy EventBus; capability/todo are
not duplicated as SubagentUpdate. The codec never interprets provider JSON or
grants exposure. Config.Observe remains a lossy UI callback.

Successful binding establishes exact historical RunEventsBound proof. Detach
and ClearRun retain proof and revoke the old publisher; later calls return
context.Canceled. Mere attachment, Publish or a failed bind is not proof.
Merge stays transparent and can recognize a genuinely bound stream after teardown.

Relay scope/tool/invocation IDs use distinct, reversible JSON-array/Base64 tuple
domains. Source retains original run/sequence/time/thread/turn and parent/scope
coordinates, including the upstream chain; the receiving core owns its envelope
sequence. ParentToolCallID uses the corresponding actual parent scope. Replay
deduplicates identical run/sequence payloads; conflicting reuse explicitly drops.
An encoded identity over 2048 bytes or a ninth source level produces a safe drop
with reason/count, without truncation or hashing. Request and event ScopeID and
ParentScopeID retain the host's real parent coordinates.

Outer capability Started follows a successful healthy BeforeDelegate and precedes
I/O. One terminal follows primary settlement, AfterDelegate and result recording;
a failed AfterDelegate cannot leave an earlier Completed fact. Publisher rejection
remains an observable primary infrastructure error or a secondary cause.

DelegationRequest.ActiveExecutionTimeout and policy MaxActiveExecutionTimeout
provide a fresh active budget for each Delegate, including continuations. The
smallest positive bound wins; zero is unlimited and negatives fail before I/O.
Retries share the same budget, Member Ask does not pause it, and Member Policy
is not overwritten. See [timing, cancellation and cleanup](./run-policy.md#delegation-active-execution-budget).

## Delegation artifact updates

`DelegationRequest.IncludeRemoteArtifacts` now also opts into ordered `DelegationArtifact.Parts` on each `DelegationArtifactCreated` event. Each event contains exactly that update's parts: `DelegationEvent.Append=false` replaces the artifact content; `Append=true` appends the supplied parts for that ArtifactID. `LastChunk=true` completes the artifact update sequence, not the delegation. Empty final chunks are retained. Text, Data, URL/file references, inline bytes, filename, media type and part metadata retain their formal A2A representation; delegation never interprets provider or host DataPart schemas to invent artifact semantics.

For example, an update carrying `[Text("A")]`, followed by `Append=true` with `[Text("B")]`, delivers those two individual payloads and yields one final remote artifact with `[Text("A"), Text("B")]`. A later `Append=false` update replaces that content. Historical Task replay only restores artifacts not already updated live and does not replay answered questions. Explicit GetTask recovery continues to retain complete extensions, reject lagging prefixes, and report incomparable content with the existing safe conflict diagnostic.

The final `DelegationResult.Artifacts` projection remains compact even when full content is requested; full final parts remain in `RemoteArtifacts`. Both final and live full projections follow `IncludeRemoteArtifacts`. With its default false value, artifact events contain no Parts or original artifact protocol Raw; when content was omitted, Event.Raw contains the explicit safe marker `{"parts_omitted":"remote_artifacts_not_requested"}`. Existing compact ID, name, description, URI, media type and artifact metadata remain unchanged. `RemotePart.Raw` means inline file bytes; artifact protocol Raw remains a separate opt-in field and is never merged into Parts or metadata.

Local full-content opt-in cannot enable the remote bridge's ExposurePolicy. Content withheld there remains absent, and enabled diagnostics arrive with the remote bridge's existing redaction. The receiver preserves explicit Data/file/metadata values rather than guessing which schema fields are safe to expose. The default does not acquire formerly hidden part text, data, inline bytes, part metadata, or an original protocol payload through the new fields.

`DelegationPolicy.MaxArtifactBytes` is applied before artifact event publication as well as at final projection. The count covers cumulative part text/URI/media type/filename strings, inline bytes, JSON Data, part/artifact metadata, artifact protocol Raw, and extension strings. URL contents are not fetched or estimated. Repeated metadata on appended updates is counted once in the cumulative view. Zero keeps the existing absence of a byte ceiling. Non-JSON-encodable Data or metadata fails closed even without a ceiling. Oversized/invalid updates produce `DelegationStreamDropped` with safe `reason=artifact_too_large` (and `max_bytes`) or `reason=artifact_invalid`, without content. Appends after a rejected update remain incomplete until a replacement or authoritative snapshot; they cannot silently resume a partial artifact. An oversized/invalid final artifact preserves the existing failure path (`artifact_too_large`) or returns `artifact_invalid`. Interrupted results retain other permitted partial artifacts while withholding rejected ones.

`MaxArtifacts` still limits only compact final Artifacts, including explicit zero. Live updates and opt-in full RemoteArtifacts remain complete. A truncation now emits a safe `DelegationStreamDropped` with `reason=artifact_result_limit`, `omitted_count`, and `max_artifacts`.

Mapped events, terminal buffering, EventBus replay and each subscription, Service observers/hooks, final results, and all Service result accessors own independent nested copies. JSON-shaped maps/slices/bytes and typed/named containers, arrays, pointers and exported struct fields are copied without a JSON round trip. A failed JSON encoding never falls back to sharing mutable source data. Callers must not mutate an input concurrently with Publish; subsequent mutation is isolated. Backpressure diagnostics clear Parts, status parts, text, errors and other semantic payloads.


## Security boundaries

- Drivers remain unaware of A2A and delegation.
- The bridge and delegation service never bypass `Runner` to dispatch a Driver.
- Remote protocol dumps are not appended to a leader's raw stdout/stderr.
- Full live/final remote artifact content requires IncludeRemoteArtifacts and remains bounded by remote ExposurePolicy and local artifact limits; URL references do not trigger fetching.
- Bearer tokens do not enter runtime-service reports, run metadata, or serialized MCP declarations.
- Hosts remain responsible for network exposure, authentication, authorization, tenant policy, and durable storage.

## Non-goals

Core does not provide automatic remote-agent discovery, A2A-to-Driver routing, team role configuration, business workflow planning, production push infrastructure, built-in tenant/auth policy, or durable task persistence. These remain explicit host responsibilities composed above the six core nouns: Agent, Thread, Stream, Event, Result, and Driver.
