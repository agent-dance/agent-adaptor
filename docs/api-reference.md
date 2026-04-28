# Public API Reference

This is the current host-facing surface of `github.com/agent-dance/agent-adaptor`.
It is intentionally a map, not a replacement for godoc.

## Construction

Use `Build(opts...) (SDK, error)` when callers need to handle configuration errors. Use `New(opts...) SDK` when fail-fast setup is acceptable.

Required:

- `WithDefaultAgent(binding)` — every SDK has an explicit default binding.

Optional SDK-level injections:

- `WithAgent(name, binding)` — named binding; `"default"` is reserved.
- `WithSessionStore(store)` — enables stateful `SessionRequest` modes.
- `WithWorkspaceManager(manager)` — resolves `WorkspaceSpec` into a lease.
- `WithSkillProvider(provider)` / `WithSkillSet(set)` — skill resolution.
- `WithSkillMaterializer(materializer)` — custom skill materialization.
- `WithRuntimeServiceManager(manager)` — runtime services.
- `WithEventBuffer(runBuf, streamBuf, policy)` — event backpressure.

Built-in bindings:

- `codex.New(agentadaptor.CodexConfig, opts...)`
- `claude.New(agentadaptor.ClaudeConfig, opts...)`
- `cursor.New(agentadaptor.CursorConfig, opts...)`

Each returns a configured `AgentBinding`. Use `NewAdapter()` only when writing lower-level tests or custom binding plumbing.

## Execution

`SDK` and `Runner` expose the same execution contract:

```go
Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error)
Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error)
```

`sdk.Run` / `sdk.Start` use the default agent. `sdk.Agent(name)` returns a `Runner` for a named binding. There is no registry-first execution path.

`RunHandle` exposes:

- `Events()` — operational `RunEvent` stream: raw chunks, transcript items, spawn/runtime/lifecycle metadata.
- `StreamEvents()` — normalized token/tool/reasoning/HITL stream payloads when `WithStreaming()` is enabled.
- `RunID()` — stable SDK run id, available immediately after `Start`.
- `Wait(ctx)` — final `RunResult`; check returned error first, then `RunResult.Failure`.
- `Cancel(ctx)` — cancel the in-flight run.
- `DecisionRequests()` / `ResolveDecision(...)` — async HITL channel mode.

## Run Options

Session:

- `WithSession(req)`
- `WithSessionKey(namespace, key)` — `continue_or_start`
- `WithContinueSession(id)` — exact `SessionID`
- `WithNewSession(namespace, key)`
- `WithForkSession(fromID, namespace, key)`

Context and injection:

- `WithWorkspace(spec)`
- `WithRuntimeServices(services...)`
- `WithSkills(refs...)`
- `WithMCP(cfg)`
- `WithInstructions(ref)`
- `WithMetadata(key, value)`
- `WithAgentIdentity(identity)`

Selected skill materialization is strict: invalid archives, missing
`SKILL.md`, unavailable paths, or custom materializer failures surface as a
`Run` / `Wait` error matching `ErrSkillMaterializationFailed` before the
adapter starts.

Policy, streaming, HITL:

- `WithRunPolicy(policy)`
- `WithStreaming()` / `WithoutStreaming()`
- `WithPermissionHandler(h)`
- `WithPlanReviewHandler(h)`
- `WithQuestionHandler(h)`

## Binding Defaults

`AgentOption` values on a binding establish defaults for later runs:

- `WithDefaultIdentity`
- `WithDefaultWorkspace`
- `WithDefaultSkills`
- `WithDefaultMCP`
- `WithDefaultRuntimeServices`
- `WithDefaultRunPolicy`
- `WithDefaultInstructions`
- `WithDefaultStreaming`
- `WithDefaultMetadata`
- `WithDefaultPermissionHandler`
- `WithDefaultPlanReviewHandler`
- `WithDefaultQuestionHandler`

Profile options are binding defaults too:

- `WithNativeProfile()`
- `WithDedicatedProfile(dir)`
- `WithCloneProfile(dir, opts)`
- `WithCloneProfileFrom(src, dst, opts)`

Provider-specific env in `CommonConfig.Env` still wins over profile options.

## Result Contract

`RunResult` and `DriverRunResult` keep output layers separate:

- `Output` — final assistant-facing text only.
- `RawStreams.Stdout` / `RawStreams.Stderr` — complete raw process streams for audit/debug.
- `Transcript` — structured semantic items parsed by the adapter from the official provider protocol.
- `Summary` — short host-facing label.
- `Result` — raw terminal provider result payload, when one exists.

`Output` must not contain a raw stdout dump and must not auto-concatenate `Summary` or `Result`.

## Admin

`sdk.Admin()` is control plane only:

- `Default()` / `Agent(name)` mirror the execution binding model.
- `Agents()` returns default + named binding metadata.
- `Info()`, `CheckEnvironment()`, `ListModels()`, `DetectModel()`, `GetProfile()`, `ConfigSchema()`, `GetQuota()` expose adapter diagnostics.
- `ProfileSnapshot(ctx)` reports the effective profile resource view.
- `SyncProfile(ctx)` materializes supported profile resources for the binding defaults without starting a run; unsupported resource families must be reported as warnings/errors rather than managed.
- `ListSkills()` reports the effective catalogue / selected set.
- `SetSelectedSkills(ctx, keys)` installs a process-local selected-key override for that bound agent. It is not persistent storage.

## Adapter Surface

Custom adapters implement:

```go
type DriverAdapter interface {
	Descriptor() DriverDescriptor
	ValidateConfig(cfg any) error
	Run(ctx context.Context, req DriverRunRequest, sink EventSink) (DriverRunResult, error)
}
```

Optional extension interfaces include environment/model/profile/quota/config-schema probes, `SkillAwareDriver`, `StreamAwareDriver`, and `SessionCodecAwareDriver`. `SkillAwareDriver.InjectSkills` remains a public SPI hook: the SDK invokes it once per run after skill resolution and before adapter `Run`; built-in adapters keep it non-destructive and do profile-local materialization inside `Run` after resume guards pass. Adapter capability declarations must match the behavior actually implemented, especially `DriverDescriptor.RunPolicyCaps`.
