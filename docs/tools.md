# Host-defined Tools

Host-defined Tools let an application add typed Go functions to an Agent. The
application describes Tools; it does not construct an MCP server, choose a
transport, distribute a bearer token, or manage a second service lifecycle.

## Define and install a Tool

```go
type LookupInput struct {
	Key string `json:"key" jsonschema:"required"`
}

type LookupOutput struct {
	Title string `json:"title"`
	State string `json:"state"`
}

lookupIssue := tool.Define(
	"lookup_issue",
	"Look up one issue by key.",
	func(ctx context.Context, in LookupInput) (LookupOutput, error) {
		return issues.Lookup(ctx, in.Key)
	},
	tool.Title("Issue lookup"),
	tool.ReadOnly(),
	tool.Idempotent(),
	tool.Revision("lookup_issue/v1"),
)

agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithTools(lookupIssue),
)
defer agent.Close(shutdownCtx)

result, err := agent.Run(ctx, "Check whether ISSUE-42 is still open.")
```

`tool.Define` accepts heterogeneous typed handlers through the sealed
`tool.Definition` value. A handler may be called concurrently and must honor
its `context.Context`.

## Construction and merge semantics

`WithTools` returns `adaptor.Option`, so using it in `Run` or `Stream` is a
compile error. The Tool set is part of the Agent's stable capability and
authorization surface, not a per-call setting.

The option replaces the whole set. The final `WithTools` supplied to `New`
wins, and `WithTools()` explicitly clears an earlier declaration. Definitions
are validated and frozen before the first provider process can start. Empty
names, invalid names, empty descriptions, nil handlers, duplicate names,
invalid schemas, and contradictory annotations fail the run before Driver
dispatch.

To expose a different Tool set, construct a different Agent. There is no
mutable registry or runtime `Register` operation.

## Schemas

Input and output schemas are inferred from the handler's Go types and their
`json` and `jsonschema` tags. The input type must encode as a JSON object. The
output may be an object, array, scalar, or another JSON-compatible typed value.
Inputs are validated before the handler runs, and outputs are validated before
they cross the provider boundary.

Use `tool.InputSchemaJSON` or `tool.OutputSchemaJSON` when a schema is maintained
outside Go:

```go
tool.Define(
	"lookup_issue",
	"Look up one issue by key.",
	handler,
	tool.InputSchemaJSON(inputSchema),
	tool.OutputSchemaJSON(outputSchema),
)
```

Overrides must be local, standard JSON Schema documents. Network references
are rejected; schemas cannot cause the Agent to fetch remote content during
construction or execution.

## Errors and cancellation

Return `tool.Reject` for an expected failure the model can correct:

```go
if !issues.Exists(in.Key) {
	return LookupOutput{}, tool.Reject(
		"not_found",
		"Choose an issue key that exists in this repository.",
	)
}
```

The code is a stable machine category and the message is safe to show to the
model. Ordinary Go errors, schema-invalid outputs, and panics are treated as
internal failures and are replaced with a generic message. Handler error text
never becomes provider-visible by accident. Only errors created by
`tool.Reject` are trusted for model-visible delivery; implementing a lookalike
method on an application error cannot opt into that path. `tool.AsRejection`
recognizes a rejection (including through wrapping) without exposing its
private concrete type. Context cancellation and the runtime's bounded handler
deadline cancel the handler context.

Annotations created with `ReadOnly`, `Destructive`, `NonDestructive`,
`Idempotent`, `OpenWorld`, and `ClosedWorld` are behavioral hints. The paired
options preserve the difference between an unspecified hint and an explicit
`false`; in particular, MCP defaults destructive and open-world hints to
`true`. Annotations are not access control, risk proof, or
a second approval mechanism. Provider approvals continue through the one
typed Event stream and `ApprovalRequest` contract.

## Threads and semantic revisions

The Tool catalog fingerprint covers the sorted names, titles, descriptions,
canonical input/output schemas, annotations, and semantic revisions. It never
uses a function or closure address and never includes authentication secrets.

Every Tool used by a stateful Thread must set `tool.Revision`. Change the
revision whenever handler behavior changes without a corresponding descriptor
or schema change. A missing revision fails a Thread before the Driver starts.
An unchanged catalog can resume the Thread and reuse a compatible persistent
provider process; a changed catalog or revision safely produces a different
compatibility identity. The concrete loopback URL and per-Agent bearer
environment-variable name remain part of MCP/profile materialization. Only the
separate session compatibility fingerprint replaces those ephemeral allocation
details with the catalog fingerprint. `ProfilePayload.Fingerprint` therefore
continues to identify the exact provider-visible payload, while
`ProfilePayload.SessionFingerprint()` is the resume/persistent-process guard.
Reconstructing an Agent after a host restart can resume an unchanged Tool
catalog while still rewriting the provider profile with the new real endpoint
and credential carrier. The Driver SPI requires resumed invocations to refresh
the complete current request rather than rely on cached MCP or profile
bindings.

For the four built-in Drivers, the provider-native MCP file is written to an
SDK-owned, Agent/identity-specific clone profile. The configured/native profile
is only the source for settings, skills, existing MCP declarations, and linked
authentication files; it is never modified by `WithTools`. Explicit profile
selection remains a construction concern, while the isolated execution clone
is an internal safety boundary. Its random directory is normalized back to the
stable source-profile identity only for Thread compatibility, just like the
ephemeral loopback port. This prevents two host processes from racing on one
provider MCP file without weakening the concrete request passed to the Driver.
The stable view also fingerprints the copied settings, MCP declarations, and
skills: changing those materialized resources safely prevents resume, while
linked authentication rotation remains outside the durable fingerprint.

`WithSpawn` replaces only the provider process. It does not restart the
Agent-owned Tool runtime.

## Lifecycle and security

The internal runtime is lazy: its listener is created during pre-launch
resource resolution, not while defining the Tool. It has these properties:

- numeric IPv4 loopback binding only;
- authenticated Streamable HTTP with an Agent-specific high-entropy bearer
  token and an independently random per-Agent environment-variable name;
- exact Host and Origin validation, request/header limits, bounded global
  concurrency, handler deadlines, panic recovery, and graceful shutdown;
- secrets delivered only through the Driver subprocess environment, never in
  the endpoint URL, runtime report, run metadata, logs, Result, or fingerprint;
- one process-local gateway with a separate immutable catalog selected by each
  Agent's bearer token, plus a separate SDK-owned execution profile per
  Agent/identity so different host processes cannot overwrite one another's
  provider configuration;
- `Agent.Close(ctx)` cancels admitted runs, closes provider processes to
  unblock them, drains them, reaps any late-created writer, removes isolated
  execution profiles, and only then revokes the Tool registration; a deadline
  leaves cleanup retryable instead of caching a partial close.

`Inspect().ProfileState` and `SyncProfile` continue to describe the configured
source profile and its public desired resources. The private clone is created
only for execution and is not a second consumer-facing profile identity.

The bearer token selects a Tool catalog inside one host process. It is not a
tenant identity and does not replace application authorization. A handler
captures the host services and authority it needs through its Go closure. It
does not receive invented Run, Thread, workspace, identity, or policy metadata
that the shared provider transport cannot prove.

## Existing MCP servers

`WithMCP` remains the advanced path for existing, remote, or separately
managed MCP servers. It can be composed with `WithTools`; the hosted Tool
server is appended through the existing runtime-service-to-MCP resolution
path. A per-call `WithMCP()` clear does not remove construction-time Tools.
Using the reserved hosted server key from an explicit MCP declaration is
rejected before Driver launch rather than silently overriding either server.
An explicit or runtime-published MCP server is also rejected if it aliases the
private environment-variable name carrying this Agent's hosted Tool bearer
token. The name is unpredictable per Agent, so a copied source profile cannot
predeclare the alias either.
Provider profile materialization applies the same fail-closed rule when that
key already belongs to an external entry copied into the isolated execution
profile. Ownership markers and rendered-content fingerprints ensure cleanup
never adopts or deletes a user-modified entry.

Tool-call Events and Transcript entries still come only from each Driver's
official provider-protocol parser. The internal runtime does not publish a
second Event stream or synthesize provider observations.

## Internal implementation and dependency choice

MCP protocol hosting is deliberately confined to `internal/toolruntime`; the
existing provider profile projection remains in `internal/mcpruntime`:

```text
tool.Definition
  -> Agent-owned immutable Tool registration
  -> authenticated loopback MCP Streamable HTTP gateway
  -> existing runtime/MCP payload resolution
  -> existing Driver profile materialization
  -> provider CLI
```

The implementation pins the official
`github.com/modelcontextprotocol/go-sdk` v1.7.0 rather than a handwritten
JSON-RPC server.

Dependency selection:

1. The official SDK materially improves protocol negotiation, Streamable HTTP,
   schema projection, cancellation, request-size enforcement, and conformance
   reliability.
2. It is maintained by the MCP project, publishes versioned releases and
   compatibility documentation, and has an established issue and security
   response surface.
3. Its imports and types are localized to `internal/toolruntime`; public Tool,
   Driver, Event, and Result contracts do not expose SDK or MCP types.

The hermetic end-to-end test runs a real child process through the Cursor
Driver. That fixture reads the isolated materialized provider MCP profile,
resolves the bearer environment reference, performs MCP discovery/list/call,
emits official provider stream records, and verifies unauthorized access. It
then closes the Agent, forces a different loopback port, reconstructs an Agent
against the same Thread store, resumes a third turn, proves the source profile
was never polluted, and verifies both endpoints and isolated profiles are
reclaimed. It makes no paid provider call.
