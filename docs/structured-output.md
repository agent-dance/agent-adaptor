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
choose an enforcement mode. For every schema request, core applies one fixed
rule before invoking the Driver:

1. use provider-native JSON Schema enforcement when the selected transport and
   policy support it;
2. otherwise inject exact-JSON instructions and validate locally;
3. fail before process launch only when neither mechanism is supported.

Explicit approval `Ask` policies additionally require `WorksWithHITL` for the
selected mechanism.

The consumer's choice between `Run` and `Stream` does not select the provider
transport. The unified invocation pipeline prefers a Driver's rich transport,
then negotiates its batch transport when structured output is not supported by
that rich transport. A `Stream` therefore remains usable but may receive fewer
incremental provider events. If neither transport can honor the request, the
run fails before the Driver is invoked with
`adaptor.ErrStructuredOutputUnsupported`.

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
| Codex | yes | yes | no; batch is negotiated | no |
| Claude | yes | yes | yes | no |
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

With `SchemaReturnInvalid`, the run returns `*Result, nil`, while
`result.Decode(&value)` reports the validation diagnostics. This option changes
only the run verdict; it does not make invalid JSON decodable.

`Run` and `Stream.Result` share the same negotiation, validation, and decode
surface. Changing an output schema does not change Thread compatibility or
split an otherwise compatible conversation.

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
