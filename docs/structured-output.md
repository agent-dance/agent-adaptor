# Structured output

Structured output is a per-invocation JSON Schema contract for the final
assistant business value. It is independent of provider protocol envelopes,
`Result.Raw()`, `Result.Transcript()`, `Result.Summary`, and the provider
terminal payload.

## Public API

The shortest path accepts any `adaptor.Runner`, so an `Agent` and a `Thread`
use the same helper:

```go
type ProjectMetadata struct {
	ProjectName          string   `json:"project_name"`
	ProgrammingLanguages []string `json:"programming_languages"`
}

value, result, err := adaptor.RunAs[ProjectMetadata](
	ctx,
	runner,
	"Extract project metadata from this repository.",
)
if err != nil {
	return err
}
fmt.Println(value.ProjectName, result.RunID)
```

`RunAs[T]` prepends `WithSchema[T]()` to the supplied call options, calls
`Runner.Run`, and decodes the validated value. In ordinary code this is the
only structured-output API needed. A later explicit schema option is useful
only when the schema needs metadata or generation customization:

```go
value, result, err := adaptor.RunAs[ProjectMetadata](
	ctx,
	runner,
	prompt,
	adaptor.WithSchema[ProjectMetadata](adaptor.SchemaName("project_metadata")),
)
```

Schema derivation failures are sticky: a later explicit schema option does not
clear an error produced while deriving the implicit `WithSchema[T]()` schema.

On an execution error, `RunAs` returns the zero value of `T` together with the
normal `Runner.Run` result/error pair. On a decode error, it returns the
available `*Result` with that error.

For event consumption or manual decode, attach a schema to `Run` or `Stream`:

```go
stream := runner.Stream(ctx, prompt,
	adaptor.WithSchema[ProjectMetadata](
		adaptor.SchemaName("project_metadata"),
	),
)
for event := range stream.Events() {
	_ = event
}
result, err := stream.Result()
if err != nil {
	return err
}

var value ProjectMetadata
if err := result.Decode(&value); err != nil {
	return err
}
```

For a schema owned outside Go, use the raw-document escape hatch. The byte
slice is copied when the option is built.

```go
result, err := runner.Run(ctx, prompt,
	adaptor.WithSchemaJSON(schemaBytes,
		adaptor.SchemaDescription("Release readiness report"),
	),
)
```

`WithSchema[T]` and `WithSchemaJSON` return `CallOption`: they are valid only
on `Run` and `Stream`, never on `adaptor.New`.

## Schema options

Options are applied in order. A later option for the same setting wins.

| Option | Contract |
|---|---|
| `SchemaName(name)` | Set the trimmed provider-facing schema name. |
| `SchemaDescription(text)` | Set the trimmed provider-facing description. |
| `SchemaReturnInvalid()` | Keep the run successful when the final value is invalid; `Result.Decode` still reports the validation error. |
| `SchemaInlineReferences()` | Inline generated references. Recursive Go types cannot use this option. |
| `SchemaAllowAdditionalProperties()` | Relax the generator's default strict-object behavior. |
| `SchemaRequireExplicitTags()` | Require fields only when marked `jsonschema:"required"`. |
| `SchemaUseGoComments(base, path)` | Add Go comments as descriptions. Both arguments are required. |

The four generation options affect `WithSchema[T]`. They intentionally have
no effect on `WithSchemaJSON`, because that function receives an already-owned
schema document. Name, description, and invalid-result policy apply to both
constructors.

Schema derivation rejects Go shapes that cannot be represented safely, such as
functions, channels, unsafe pointers, complex values, and maps with non-string
keys. Generated and supplied documents are normalized and compiled as JSON
Schema Draft 2020-12 before the Driver is invoked. Malformed JSON, multiple JSON
values, an empty document, an unsupported format, or a schema compile failure
all match `adaptor.ErrInvalidOutputSchema`.

## Automatic capability negotiation

Every Driver declares `driver.Descriptor.StructuredOutput`. Consumers do not
choose an enforcement mode. Core normalizes schema once before run resources,
then freezes feasible transport candidates and each candidate's mechanism:

1. prefer provider-native JSON Schema when that transport and policy support it;
2. otherwise select prompt instructions plus local validation;
3. reject before profile, workspace, runtime, skill or Thread lease acquisition
   if no candidate can honor the request.

After service attachment, observation demand selects only among these candidates.
Only then are any selected prompt-schema instructions assembled. No schema
normalization, resource resolution or materialization is repeated.

The SPI has optional `NativeHITL` and `PromptValidateHITL` pointers to
`StructuredOutputHITLCapability{Permission, PlanReview, Question bool}`. Each
non-nil matrix checks all effective Ask kinds, including inherited Permission
and PlanReview Ask; Question defaults to automatic denial. Every required kind
also needs the ordinary `RunPolicyCaps` Ask capability. An all-false matrix
rejects Ask even when the old bool is true. A nil matrix retains the legacy
`WorksWithHITL` check for explicitly selected Ask, independently of the other
mechanism. Non-Ask kinds and calls without schema do not use these matrix fields.

Drivers adopting a matrix may cause a schema call with previously unset
Permission to choose prompt validation or become unsupported. Core does not
silently approve a question to preserve native enforcement.

The consumer's choice between `Run` and `Stream` does not select the provider
transport. Initial selection uses the captured Driver configuration and its
StreamSupport/StreamCapability/providerRichTransport contract. When rich is
available, core may also validate batch as a candidate; it never invents rich
support from a zero descriptor. Each candidate preserves applicable Ask, even
for an explicit Ask without schema. Attachment demand scores only these feasible
candidates, with ties and zero demand preserving the initial selection. A Stream
can therefore use batch and receive fewer provider deltas while retaining the
same public execution contract. If no candidate honors schema, execution fails
before resources with `adaptor.ErrStructuredOutputUnsupported`.

Hosts may inspect the public Driver descriptor for diagnostics, but there is no
structured-output choice to expose in their UI:

```go
d := cursor.Driver(cursor.Config{Model: "gpt-5"})
caps := d.Descriptor().StructuredOutput
agent := adaptor.New(d)
_ = caps // diagnostics only; adaptor selects the mechanism automatically
```

Current built-in declarations are:

| Driver | Native schema | Prompt validation | Rich provider transport | Explicit HITL `Ask` |
|---|---:|---:|---:|---:|
| Codex | yes | yes | yes; app-server and batch | no |
| Claude | yes | yes | yes | native Question/PlanReview; Permission uses prompt |
| Cursor | no | yes | yes | no |
| CodeBuddy | yes | yes | no; batch is negotiated | no |

Cursor therefore falls back automatically to local validation. Prompt
validation parses only the final assistant `Result.Text` as one exact JSON
value; it does not strip Markdown fences, search raw stdout, or guess through
provider envelopes. Provider-native output is also revalidated by the SDK
before its raw JSON is made available to `Result.Decode`.

For Codex batch runs, `codex exec --json --output-schema` carries the native
JSON value in the `text` field of the last completed `agent_message` item. The
Driver keeps all completed assistant messages in `Result.Text`, keeps
`turn.completed` as the provider terminal payload in `Result.Raw()`, and gives
only that last assistant value to the shared schema validator. A top-level
`result` envelope is not part of this wire contract and is never treated as
structured output. Missing or failed terminals, malformed protocols, nonzero
exits, signals, timeouts, and business failures cannot yield a native
structured-output candidate.

## Claude schema and approvals

Claude declares NativeHITL={Permission:false, PlanReview:true, Question:true}
and PromptValidateHITL={Permission:true, PlanReview:true, Question:true}.
WorksWithHITL remains conservatively false; each precise matrix takes precedence
for its own mechanism. Ordinary Permission Ask without schema remains supported.

| Raw policy | Schema choice | Interactive activation |
|---|---|---|
| Zero policy | Prompt; inherited Permission/PlanReview Ask | Existing observational transport |
| QuestionAsk, Permission unset | Prompt; inherited Permission Ask | Bidirectional, including real Permission requests |
| PermissionAutoApprove, QuestionAsk | Native; inherited PlanReview Ask supported | Bidirectional |
| PermissionAutoApprove, PlanReviewAsk | Native | Bidirectional |

This does not silently approve permissions or alter the raw-policy rule that
activates interactive transport. For native schema with questions, explicitly
set Permission to ApprovalAutoApprove and Question to QuestionAsk, and answer
through OnApproval or the typed request event as usual. Zero-policy tests are
not evidence of a Permission round-trip.

Native interactive Claude runs retain stream-json input/output and the same
control-response stdin even when the resolved Request.Streaming is false.
On Thread, native schema uses a temporary process shape: the old writer exits
before replacement, and only a healthy result may prewarm a resident writer.
WithSpawn suppresses registration/prewarm. It does not promise PID reuse across
native rounds.

## Decode and failure behavior

`(*Result).Decode(v)` has two deliberately distinct paths:

1. When a schema was requested, it decodes only the value already validated by
   the structured-output pipeline. Invalid or empty structured data is an
   error.
2. Without a schema, it trims `Result.Text` and decodes that text as a JSON
   convenience. Empty text or malformed JSON is an error.

The default invalid-result policy fails the completed run with a
`*adaptor.RunError` matching `adaptor.ErrPolicyViolation`. The full audit value
is retained in `runErr.Result`:

```go
result, err := runner.Run(ctx, prompt,
	adaptor.WithSchema[ProjectMetadata](),
)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) && errors.Is(err, adaptor.ErrPolicyViolation) {
		result = runErr.Result
	}
	return err
}
```

On cancellation or another execution failure, `RunError.Result` retains any
already-validated structured data. Core does not revalidate interrupted output
and replace the original cause. If a schema was requested but validated data
is absent, `Decode` fails; it never applies the no-schema Text convenience path.

With `SchemaReturnInvalid`, the run returns `*Result, nil`, while
`result.Decode(&value)` reports the validation diagnostics. This option changes
only the run verdict; it does not make invalid JSON decodable.

`Run` and `Stream.Result` share the same negotiation, validation, and decode
surface. A schema or the per-turn Request.Streaming choice does not itself
create a new Thread identity. Transports proven compatible by the same configured
Driver/SessionCodec continue the same healthy checkpoint, including returning
from a temporary schema process to a prewarmed resident process. Actual changes
to configuration, checkpoint encoding or session environment still require the
existing configuration fingerprint, codec and Driver guards; process startup
signatures continue to check the real argv/environment. A transport hint cannot
replace these checks or force an otherwise compatible Thread to rebind.

## Security and dependencies

Schema content can be sent to the provider. Do not put secrets in names,
descriptions, enum/const values, regular expressions, examples, or Go comments
used as schema descriptions.

The implementation localizes two maintained libraries behind standard
`[]byte` and `encoding/json` types:

- `github.com/invopop/jsonschema` derives schemas from Go types;
- `github.com/santhosh-tekuri/jsonschema/v6` compiles and validates Draft
  2020-12 documents.

No third-party schema type appears in the public API, and provider-specific
flags remain inside their Driver packages.


## Native append with structured output

The resolved native append channel follows a schema-selected transport and its
prewarm/replacement process, retaining normal Thread compatibility. It never
becomes the schema validation prompt. Claude keeps its existing native
Question/PlanReview and prompt-fallback Permission rules. CodeBuddy native schema
still cannot share its control HITL transport. Nonempty CodeBuddy append validates
the whole Windows argv; cmd-shim combinations containing unsafe JSON/schema
quoting fail explicitly. Use a supported native executable or lossless PowerShell
path. Cursor nonempty append remains unsupported before startup. See
[carrier limits](./api-reference.md#31-dual-scope-options) and the existing capability
matrix; append does not grant additional HITL/schema support.
