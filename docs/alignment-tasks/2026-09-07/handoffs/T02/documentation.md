# T02 / W07 — safe, correctable Tool input failures

Source-Internal-Commits: `64163ab67ecf109d196da10a4cbac8943d8e6eff`

## Public behavior and reproduction

Previously `Definition.Invoke` wrapped only `ErrInvalidInput`, including raw
parser/schema/Go decoder error text. Hosted Tools used the generic MCP SDK
registration wrapper, which validated arguments before `Definition.Invoke` and
returned its own unsanitized schema errors to the model. An invalid value could
therefore be echoed by that transport validation.

Invalid argument failures now join `ErrInvalidInput` and the package-private
`Reject("invalid_input", safeMessage)`. Both `errors.Is` and `tool.AsRejection`
work. The handler does not run for invalid arguments. JSON syntax, schema
validation, and Go input decoding have distinct safe hints; the returned error
does not retain the original validation or decoder error. Nil context remains
a host programming error matching `ErrInvalidInput` without a correction.

```go
lookup := tool.Define("lookup", "Look up a record.",
    func(context.Context, struct {
        Key string `json:"key"`
    }) (struct{}, error) { return struct{}{}, nil })
_, err := lookup.Invoke(context.Background(),
    json.RawMessage(`{"key":"x","extra":"private value"}`))
errors.Is(err, tool.ErrInvalidInput) // true
code, message, ok := tool.AsRejection(err)
// code: "invalid_input", ok: true
// message: invalid field "extra"; use a declared field name and retry
```

Extra field diagnostics select the lexicographically first extra name across
unconditional validation causes, including nested objects. Names use at most
64 UTF-8 bytes plus an ellipsis before quoting; control characters, backslashes,
quotes, and non-printing Unicode are escaped. The entire correction stays below
512 bytes. No argument value, schema fragment, or underlying error is included.
The empty field name is handled. `anyOf` and `oneOf` branches use the generic
schema hint because another branch may declare the same field.

Hosted Tools advertise the same schemas and annotations, but register through
the official `Server.AddTool` raw handler. `Definition.Invoke` owns both input
and output validation; the transport no longer applies schema defaults or
rewrites arguments/results separately. Schema `default` remains descriptive:
omitted fields have the behavior of the declared Go input type, consistently
for direct and hosted calls. Omitted MCP arguments and `{}` reach the same empty
object behavior. Explicit `null` remains invalid for an object input.

Success retains validated JSON in both `structuredContent` and the JSON text
fallback for objects, arrays, scalars and null. Errors contain only the safe
code/message content and no structured result. Authentication, catalog routing,
concurrency, size/deadline limits and gateway cleanup are unchanged. Permission
approval remains in the existing provider/core policy path; this gateway does
not introduce approval decisions or bypass that path.

## G01 central documentation merge targets

### `docs/tools.md`, “Schemas”

Add after the validation description:

> Direct `Definition.Invoke` and hosted MCP calls share the same Tool validation.
> The transport preserves validated JSON and does not apply schema defaults.
> A schema `default` is descriptive; omitted fields follow the Go input type.

### `docs/tools.md`, “Errors and cancellation”

Add before the handler `tool.Reject` example:

> Invalid JSON arguments, input schema mismatches and Go input decoding failures
> are rejected before the handler runs. The error matches `tool.ErrInvalidInput`
> and `tool.AsRejection` returns `invalid_input` with a safe correction. Syntax
> failures request valid JSON, schema failures point to required fields, types
> and allowed values, and Go decoding failures point to input types and formats.
> An extra-field hint may quote the lexicographically first extra name, limited
> to 64 UTF-8 bytes plus an ellipsis with control and non-printing characters
> escaped. Ambiguous `anyOf`/`oneOf` failures use the generic schema hint.
> Corrections never include values, schema contents or underlying error text.

Amend the existing “Only errors created by `tool.Reject`” sentence to clarify
that the SDK creates the same private rejection for invalid arguments.
Retain the existing output-error/panic sanitization and external `As` protection
description. Cancellation keeps priority when observed during input validation
or decoding, and prevents handler invocation.

### `CHANGELOG.md`, `[Unreleased] / Fixed`

- Return safe `invalid_input` corrections for Tool JSON/schema/Go decoding
  failures while preserving `errors.Is(err, tool.ErrInvalidInput)`. Bound and
  escape extra-field names, and avoid misleading hints from alternative schemas.
- Route hosted MCP arguments through `Definition.Invoke` to prevent the MCP
  wrapper from disclosing raw validation errors. Preserve schemas, successful
  structured/JSON-text results, and consistent default/omitted-field semantics.

## Local godoc, API, and dependencies

Updated `tool/doc.go`, `tool/errors.go` (`ErrInvalidInput`) and `tool/tool.go`
(`Definition.Invoke`). There are no new public declarations, no root or Driver
AST changes, and no golden update. No dependencies or module files changed:
the implementation uses existing jsonschema/v6 structured error kinds and the
existing official MCP SDK raw registration API.

## Internal behavior deliberately not adopted

- Do not blindly echo the validator's first key: cause order is nondeterministic
  and keys can be huge or contain control characters.
- Do not treat every decoder failure as invalid JSON syntax: valid JSON can fail
  a Go type or custom decoder. Custom decoder errors cannot provide safe text.
- Do not inspect non-matching alternative branches to claim a field is illegal.
- Do not retain or forward underlying errors, schema contents, argument values,
  output validation details, handler errors or panics to the model.
- Retain the current private rejection identity and standard-unwrap-only
  `AsRejection` traversal; an external error's `As` cannot forge it.

## Verification boundary

Fixtures first failed on the G00 baseline. The original unprivileged command
also recorded loopback bind restrictions; the identical full command was rerun
with the authorized local loopback permission and exposed the actual failures.
Both packages passed 90 tests/subtests before the source/documentation commit.
Final evidence and exact counts belong to `result.json` and `evidence/`, produced
after the commit. The required command is `go test -count=1 ./tool
./internal/toolruntime`, with `-json` only for evidence collection. Supplemental
root-package and package race checks assess compatibility with existing policy
and concurrent gateway behavior.

Local execution is macOS arm64 with Go 1.26.5. All runs explicitly set
`AGENT_ADAPTOR_LIVE_CONFORMANCE=0`, `AGENT_ADAPTOR_E2E=0`, and
`AGENT_ADAPTOR_UPDATE_API_GOLDEN=0`. No provider CLI or paid/live call is used.
This task does not claim Linux/Windows, release-gate, or live acceptance.
