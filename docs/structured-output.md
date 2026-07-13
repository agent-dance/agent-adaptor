# Runtime Structured Output

Runtime structured output is a per-run JSON Schema contract for the final
assistant business value. It is separate from CLI JSON event envelopes,
`RawStreams`, `Transcript`, `Summary`, and provider terminal `Result` payloads.

## Public API

Preferred Go-host usage derives the schema from a struct:

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

Use the lower-level schema APIs only when the schema is dynamic or already
owned outside Go:

```go
schema, err := agentadaptor.JSONSchemaFor[ProjectMetadata](
	agentadaptor.SchemaInlineReferences(),
)
if err != nil {
	return err
}

res, err := sdk.Run(ctx,
	"Extract project metadata from this repository.",
	agentadaptor.WithJSONSchemaOutput(schema, agentadaptor.NativeStrictOutput()),
)
```

`RunStructured[T]` is a convenience wrapper around `Runner.Run` plus
`DecodeStructuredOutput[T]`. It is a package function rather than a `Runner`
method because Go interface methods cannot define their own type parameters.

## Examples

Codex native structured output through the default agent:

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
		Model: "gpt-5.4",
	})),
)

res, err := sdk.Run(ctx,
	"Extract project metadata from this repository.",
	agentadaptor.WithJSONSchemaOutputFor[ProjectMetadata](
		agentadaptor.NativeStrictOutput(),
	),
)
```

Claude native structured output through a named agent:

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{Model: "gpt-5.4"})),
	agentadaptor.WithAgent("claude", claude.New(agentadaptor.ClaudeConfig{
		Model: "claude-sonnet-4",
	})),
)

review, err := sdk.Agent("claude")
if err != nil {
	return err
}
res, err := review.Run(ctx,
	"Return a release-note summary for the pending changes.",
	agentadaptor.WithJSONSchemaOutputFor[ReleaseSummary](
		agentadaptor.NativeStrictOutput(),
	),
)
```

Cursor prompt-validation fallback must be explicit:

```go
cursorRunner, err := sdk.Agent("cursor")
if err != nil {
	return err
}
res, err := cursorRunner.Run(ctx,
	"Summarize repository risk as JSON.",
	agentadaptor.WithJSONSchemaOutputFor[RiskProfile](
		agentadaptor.PromptValidateOutput(),
	),
)
if err != nil {
	return err
}
if res.StructuredOutput == nil || !res.StructuredOutput.Valid {
	return fmt.Errorf("invalid structured output: %v", res.StructuredOutput.ValidationErrors)
}
```

## Modes

| Mode | Contract |
|---|---|
| `NativeStrictOutput()` | Require provider/CLI-native JSON Schema enforcement. Unsupported adapters fail before launch with `ErrStructuredOutputUnsupported`. This is the default. |
| `PreferNativeOutput()` | Use native enforcement when available, otherwise use explicit prompt+SDK validation when the adapter advertises it. |
| `PromptValidateOutput()` | Inject exact-JSON prompt instructions and validate the final adapter `Output` locally. This is weaker than native constrained output and must be requested explicitly. |

Prompt validation parses only the final adapter `Output` as exact JSON. It does
not scan `RawStreams`, strip Markdown fences, or guess JSON from protocol
wrappers.

## Built-In Adapter Matrix

| Adapter | Native JSON Schema | Prompt validation | Works with streaming | Works with HITL | Mapping |
|---|---:|---:|---:|---:|---|
| Codex | yes | yes | prompt-validation only | no | Native batch runs materialize the schema to a per-run temp file and call `codex exec --output-schema <file> --json`. The Codex parser extracts final JSON from official terminal result events. |
| Claude Code | yes | yes | yes | no | Native batch runs use `--output-format json`; streaming runs use `--output-format stream-json --verbose`. Both pass `--json-schema`, and the parser reads the terminal `structured_output`. Interactive HITL plus native structured output is rejected before launch. |
| Cursor | no | yes | yes | no | Cursor CLI exposes `json` / `stream-json` protocol envelopes but no native output-schema flag. `NativeStrictOutput()` is rejected; `PromptValidateOutput()` uses SDK prompt injection plus local validation. |

Capability gating is available through `Admin().Agent(name).Info()`:

```go
admin, err := sdk.Admin().Agent("cursor")
if err != nil {
	return err
}
caps := admin.Info().Descriptor.StructuredOutput
if !caps.JSONSchemaNative && caps.JSONSchemaPromptValidate {
	// Offer an explicit "prompt + validate" mode instead of native strict.
}
```

Cursor cannot be made provider-native by the SDK alone. `agent-adaptor` sits
outside the Cursor CLI, so it cannot pass a schema into Cursor's model request
until Cursor exposes a schema-output surface. Prompt validation remains useful
for automation, but hosts should surface it as a weaker local validation mode.

## Results And Failures

`RunResult.StructuredOutput` carries the final JSON value:

- `RawJSON`: canonical final JSON bytes.
- `Value`: decoded JSON tree for convenience.
- `Valid`: local validation result.
- `ValidationErrors`: structured validation diagnostics.
- `Source`: `native` or `prompt_validate`.
- `SchemaHash`: stable hash of the requested schema.

`Run()` and `Start().Wait()` return the same structured-output surface because
the request travels through the existing `resolvedInvocation -> DriverRunRequest
-> adapter.Run` path. Schema handling does not change session, workspace,
skills, runtime, streaming, checkpoint, or archive semantics.

Invalid schema JSON fails before adapter launch with `ErrInvalidOutputSchema`.
Unsupported mode/capability combinations fail before launch with
`ErrStructuredOutputUnsupported`. If an adapter or prompt-validation run returns
JSON that does not validate and the policy is `StructuredOutputFailRun`, the
run returns a `FailurePolicyError` on `RunResult.Failure`; use
`ReturnInvalidStructuredOutput()` to receive `StructuredOutput.Valid=false`
without marking the run failed.

`WithJSONSchemaOutputFor[T]` preserves the generated schema and its stable hash
for every adapter. Claude prepares a provider-local copy by removing root schema
metadata and inlining local references. Recursive schemas remain valid for the
public helper and prompt validation, but Claude native mode rejects recursive
local references because they cannot be safely inlined for that CLI path.

## Security Notes

Schema content may be sent to provider CLIs and may be retained according to
provider policy. Do not put secrets in schema names, descriptions, enum values,
const values, regex patterns, examples, or comments used to generate
descriptions.

## Dependency Selection

The implementation uses `github.com/invopop/jsonschema` for Go struct to JSON
Schema generation and `github.com/santhosh-tekuri/jsonschema/v6` for local
validation.

Reliability: `invopop/jsonschema` reflects ordinary Go structs into strict,
LLM-friendly JSON Schema while respecting `json` and `jsonschema` tags.
`santhosh-tekuri/jsonschema/v6` is a mature JSON Schema compiler/validator with
Draft 2020-12 support, which avoids hand-written validation and protocol drift.

Maintainability: both packages are maintained public Go libraries with
documented APIs and versioned modules. The older `github.com/alecthomas/jsonschema`
line has moved to Invopop and is not used for new work.

Localization: no third-party schema type appears in the public API. Public
entry points accept and return `json.RawMessage`/`[]byte`; generation and
validation stay inside the SDK implementation. Adapter-specific CLI flags stay
inside `codex`, `claude`, and `cursor` packages.

Rejected alternatives: `github.com/google/jsonschema-go/jsonschema` was a
strong official-backed candidate, but the current API is younger and less
tailored to strict LLM output schemas. `github.com/swaggest/jsonschema-go` is
powerful but brings a larger and more complex API surface than needed for the
SDK-owned helper layer.
