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
- `WithProfileResources(resources)` — per-run profile desired state bundle.

Runtime service MCP injection: a `RuntimeServiceManager` may append run-scoped MCP
servers after `RuntimeServiceManager.Ensure` by returning `RuntimeServiceRef`
values whose metadata includes `agentadaptor.mcp.enabled=true`. This does not
require an explicit `WithMCP` option. Recognized metadata keys are
`agentadaptor.mcp.enabled`, `agentadaptor.mcp.key`,
`agentadaptor.mcp.transport`, `agentadaptor.mcp.url`,
`agentadaptor.mcp.command`, `agentadaptor.mcp.args_json`,
`agentadaptor.mcp.env_json`, `agentadaptor.mcp.headers_json`,
`agentadaptor.mcp.bearer_token_env_var`, `agentadaptor.mcp.required`, and
`agentadaptor.mcp.required_reason`. The SDK converts enabled refs to
`MCPServerSpec`s, appends them to the effective MCP config, validates them with
ordinary MCP capability checks, and includes them in `DriverRunRequest.MCP` plus
the profile/session fingerprint. Malformed JSON metadata and duplicate MCP keys
fail the run before adapter start. Other runtime metadata remains runtime-service
metadata; it is not promoted into MCP server configuration.

- `WithAgents(specs...)`
- `WithHooks(specs...)`
- `WithProfileConfig(patches...)`
- `WithModel(model)` — per-run model override. Forwarded to the adapter as
  `DriverRunRequest.ModelOverride` and supersedes the binding model
  (`CodexConfig.Model` / `ClaudeConfig.Model` / `CursorConfig.Model`) for the
  built-in `--model` flag on this run only. Unlike `WithProfileConfig` it does
  not persist anything to the profile on disk and applies uniformly across
  drivers regardless of any `model` profile-config capability. Blank values are
  ignored (binding model stays in effect).
- `WithInstructions(ref)`
- `WithMetadata(key, value)`
- `WithAgentIdentity(identity)`

Selected skill materialization is strict: invalid archives, missing
`SKILL.md`, unavailable paths, or custom materializer failures surface as a
`Run` / `Wait` error matching `ErrSkillMaterializationFailed` before the
adapter starts.

Profile resource status: skills, MCP, agents, hooks, instructions, and explicit
native config patches all have materialization paths in the built-in adapters.
`ProfileSnapshot` and `SyncProfile` report the actual materialization state,
including warning/error paths for unsupported or fallback fields. The target
design remains a maximum-capability envelope: portable core fields, typed
extended fields, explicit fallback/unsupported reporting, and provider-native
escape hatches.

Current public resource envelope:

- `AgentSpec`: portable core `Key`, `RuntimeName`, `Description`,
  `Instructions` / legacy `Content`, and `SourcePath`; extended fields include
  model, reasoning effort, tool policy, permission/sandbox mode, MCP servers,
  skills, nested hooks, and `Native`. Built-in adapters materialize portable
  core agents to provider-native agent directories. Codex writes TOML custom
  agents; Claude Code and Cursor write Markdown/YAML agents. Unsupported
  extended fields are reported in `ResourceSnapshot.Warnings`.
- `HookSpec`: canonical `HookEvent`, `MatcherSpec`, `HookHandler`, timeout,
  fail policy, status message, disabled flag, plus legacy command-hook
  `Matcher` / `Command` / `Args` / `Env`. Built-in adapters materialize
  command-hook core to provider-native hook config. Claude Code additionally
  accepts prompt/http/mcp_tool/agent handlers; Cursor accepts prompt handlers;
  Codex accepts command handlers. Unsupported events/handlers fail sync/run
  instead of silently succeeding.
- `InstructionsBundleRef`: `ID`, `Path`, `Content`, `Fingerprint`, `Scope`,
  `Mode`, and `Native`. Codex materializes profile instructions to
  `AGENTS.md` / `AGENTS.override.md` under the effective `CODEX_HOME`; Claude
  Code materializes them to `CLAUDE.md` under the effective
  `CLAUDE_CONFIG_DIR`. Cursor has no stable profile-level rules file in the
  public CLI config, so profile sync uses an SDK-managed fallback file; during
  `Run`, project/local scoped Cursor instructions are materialized as
  `.cursor/rules/<id>.mdc` in the effective workspace. Run-scoped or otherwise
  unmapped bundles are prompt-injected and reported as fallback.
- `ProfileConfigPatch`: preferred `Capability + Values`, with
  `NativeConfigPatch` and legacy `FileKind` / `Path` / `Section` for explicit
  provider-native patches. Built-in adapters materialize explicit
  provider-native JSON/TOML patches guarded to the effective profile root and
  an allowlisted set of capability patches: Codex `model`,
  `reasoning_effort`, `sandbox`, `approval`; Claude Code `model`, `effort`,
  `permission`, `env`; Cursor `sandbox`, `approval`, `permissions`, `display`.
  The same allowlist is exposed from `AgentAdmin.ConfigSchema()` as
  `profile_config.*` fields with capability metadata. Unsupported capabilities
  remain desired-state and are reported in snapshot warnings/errors.
- `ResourceSnapshot`: every resource kind reports both `Support` and
  `Materialization`, so hosts can distinguish native managed, file managed,
  prompt-injected fallback, and unsupported desired state.

Policy, streaming, HITL:

- `WithRunPolicy(policy)`
- `WithStreaming()` / `WithoutStreaming()`
- `WithPermissionHandler(h)`
- `WithPlanReviewHandler(h)`
- `WithQuestionHandler(h)`

Structured output:

- `WithJSONSchemaOutputFor[T](opts...)` — preferred Go-host path. The SDK derives a JSON Schema from `T`, deep-copies it into the invocation, and returns the final JSON on `RunResult.StructuredOutput`.
- `JSONSchemaFor[T](schemaOpts...)` — derive only the schema bytes when the host needs to inspect or cache the schema before a run.
- `WithJSONSchemaOutput(schemaJSON, opts...)` / `WithJSONSchemaOutputFile(path, opts...)` — low-level escape hatches for dynamic schemas or non-Go hosts.
- `WithOutputSchema(schema)` — fully specified request for advanced callers.
- `NativeStrictOutput()` — default; require provider/CLI-native schema enforcement and fail before adapter launch when unsupported.
- `PreferNativeOutput()` — use native enforcement when available, otherwise explicit prompt+SDK validation when the adapter advertises it.
- `PromptValidateOutput()` — inject exact-JSON instructions and validate the final adapter `Output`; this is not provider-native constrained decoding.
- `DecodeStructuredOutput[T](res)` / `RunStructured[T](ctx, runner, prompt, opts...)` — decode the validated final JSON into a Go value.

Adapter support is exposed through `DriverDescriptor.StructuredOutput`. Current built-ins:

| Adapter | Native JSON Schema | Prompt validation | Streaming | Notes |
|---|---:|---:|---:|---|
| Codex | yes, batch `codex exec --output-schema` | yes | no native streaming guarantee | app-server schema support is not advertised until verified |
| Claude Code | yes, print-mode `--json-schema` with `json` or `stream-json` output | yes | yes, native | interactive HITL + native schema is rejected; recursive local references are unsupported in native mode |
| Cursor | no | yes | yes for prompt validation | Cursor CLI has JSON envelopes but no native schema-output surface |

Example:

```go
type ProjectMetadata struct {
	ProjectName          string   `json:"project_name"`
	ProgrammingLanguages []string `json:"programming_languages"`
}

res, err := sdk.Run(ctx,
	"Extract project metadata from this repository.",
	agentadaptor.WithJSONSchemaOutputFor[ProjectMetadata](
		agentadaptor.NativeStrictOutput(),
		agentadaptor.StructuredOutputName("project_metadata"),
	),
)
if err != nil {
	return err
}
meta, err := agentadaptor.DecodeStructuredOutput[ProjectMetadata](res)
```

Use `PromptValidateOutput()` explicitly for Cursor or for other runs where a weaker prompt+local-validation contract is acceptable. Schema names, descriptions, enum values, const values, regex patterns, and examples may be sent to provider CLIs; do not put secrets there. More examples and the built-in adapter matrix are in [`structured-output.md`](./structured-output.md).

## Binding Defaults

`AgentOption` values on a binding establish defaults for later runs:

- `WithDefaultIdentity`
- `WithDefaultWorkspace`
- `WithDefaultSkills`
- `WithDefaultMCP`
- `WithDefaultProfileResources`
- `WithDefaultAgents`
- `WithDefaultHooks`
- `WithDefaultProfileConfig`
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
`CloneProfileOptions.IncludeAuth` keeps the legacy copy behavior; for local
OAuth-backed CLIs use `CloneProfileOptions{AuthMode: CloneProfileAuthLink}` to
share the native auth file with an isolated clone.

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
- `SyncProfile(ctx)` materializes supported profile resources for the binding defaults without starting a run; current built-in coverage includes skills, MCP, agents, hooks, instructions, and config capability patches. Unsupported resource families must be reported as warnings/errors rather than managed.
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
