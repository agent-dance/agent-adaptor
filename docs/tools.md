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

Direct `Definition.Invoke` and hosted MCP calls share the same Tool validation.
The transport preserves validated JSON and does not apply schema defaults.
A schema `default` is descriptive; omitted fields follow the Go input type.
Omitted MCP arguments behave like `{}`; explicit `null` is invalid for an
object input.

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

Invalid JSON arguments, schema mismatches and Go decoding failures are rejected
before the handler runs. The error matches `tool.ErrInvalidInput` and
`tool.AsRejection` yields `invalid_input` with a safe correction. Extra-field
hints quote the lexicographically first unconditional extra name, limited to
64 UTF-8 bytes plus an ellipsis with control/non-printing characters escaped.
Ambiguous `anyOf`/`oneOf` branches use a generic schema hint. Corrections omit
argument values, schemas and original error text. Observed cancellation keeps
priority and prevents handler invocation.

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
never becomes provider-visible by accident. Only the package-private rejection
created by `tool.Reject` or SDK input validation is trusted for model-visible
delivery; implementing a lookalike
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
SDK-owned execution clone. Explicit Dedicated profiles with non-empty Tools
select a persistent source/Driver/identity namespace; other selections retain
per-Agent temporary clones. The configured/native profile
is only the source for settings, skills, existing MCP declarations, and linked
authentication files; it is never modified by `WithTools`. Explicit profile
selection remains a construction concern, while the isolated execution clone
is an internal safety boundary. Its execution directory is normalized back to the
stable source-profile identity only for Thread compatibility, just like the
ephemeral loopback port. This prevents two host processes from racing on one
provider MCP file without weakening the concrete request passed to the Driver.
The compatibility view includes observed copied settings, MCP declarations and
skills, while linked authentication rotation is outside the durable fingerprint.
Core takes an immutable final snapshot after the unique skill resolver/injection
and before Thread fingerprint/store/Driver execution. The early claim checks
ownership and tree safety; proven managed links may await cache reconstruction
by that resolver. The final snapshot includes actual contents and modes, resolved
skill source contents, ordinary MCP and unknown settings. Unproven links and
unsafe or unreadable final resources fail explicitly.

Only the provider's known configuration files are normalized as JSON/TOML
objects. Skill attachments and other manifest resources are fingerprinted as
exact bytes, regardless of extension: JSON arrays/scalars and intentionally
malformed example files remain valid skill assets. Any byte change in those
assets changes compatibility; malformed provider configuration still fails.

A Driver may defer physical profile reconciliation until Run. Its compatibility
view projects only proven managed replacements/prunes from the resolved sources,
without resolving or writing resources again; unrelated resources remain included.
A copied tree's source-path marker alone cannot prove its contents or modes: an
unprovable planned prune fails with profile.ErrUnsafe instead of hiding that tree
from the snapshot. Read failures are not treated as missing paths. MCP JSON/TOML writes preserve
an existing regular file's permissions, while new files retain the materializer
default. Non-regular targets and inspection errors fail explicitly, so an SDK
rewrite neither changes a 0600 file to 0644 nor hides real external mode drift.
A not-yet-created file's snapshot uses the platform-observable form of that
unchanged default. On Windows, Go reports the read-only/writeable attribute as
0444/0666 rather than POSIX owner/group bits; this does not change ACLs or write
permissions. If the OS refuses replacement of a read-only file, the error remains
observable without temporarily widening its permissions.
Deferred skills directories likewise use the platform's observed directory mode,
so materialization does not itself change a cold-resume fingerprint. A managed
skills parent that exists as an ordinary file fails explicitly; it is not treated
as an absent skills directory.
Ordinary MCP overwrite/prune requires the existing manifest's exact provider/path/
rendered-content proof. Unknown fields or same-key changes cannot be erased by
projecting desired configuration. Authentication/session data and only proven
Agent-owned volatile MCP allocations remain excluded or normalized as documented.

The per-run digest contributes to the Thread guard, concrete ProfilePayload
fingerprint and stable SessionCompatibilityFingerprint, together with all original
configuration/codec/environment dimensions. A compatible per-turn transport
choice is not independently hashed into Thread identity; actual process shape
still follows the Driver's private signature and session guard. The digest is
never cached as
a mutable Agent-wide compatibility value. A coordination gate for the actual
execution.Dir spans claim, unique resolution, snapshot and Driver execution;
different isolated directories can run concurrently, while runs sharing one
profile cannot change each other's files midway through the snapshot.

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
  unblock them, drains them, reaps any late-created writer, removes proven-owned
  MCP projections, revokes the Tool registration, then deletes temporary clones
  or releases persistent ownership. Failed phases remain retryable.

`Inspect().ProfileState` and `SyncProfile` continue to describe the configured
source profile and its public desired resources. The private clone is created
only for execution and is not a second consumer-facing profile identity.

The bearer token selects a Tool catalog inside one host process. It is not a
tenant identity and does not replace application authorization. A handler
captures the host services and authority it needs through its Go closure. It
does not receive invented Run, Thread, workspace, identity, or policy metadata
that the shared provider transport cannot prove.

## Persistent Dedicated profiles

When non-empty `WithTools` is combined with explicit `WithProfile(profile.Dedicated(source))`, supported built-in Drivers execute in a persistent sibling clone. The source must already exist and is read without calling a provider initializer. Its canonical source path, Driver type, and all four identity fields select a private namespace:

`<canonical-source>.hosted-tools/v1/<sha256-framed-key>/profile`

Thread keys never become directory names. Source path aliases resolve to the same ownership partition. Empty identity is a real shared partition; another Agent using it gets `profile.ErrInUse` immediately, including within the same OS process. Different ID, tenant, profile or name fields select distinct partitions.

The first use copies settings, MCP and skills once and shares only declared provider auth files through the existing AuthLink policy. A normal close and a new Agent with the same configuration retain provider session files, and renew the hosted gateway URL, bearer token and environment carrier. Later source configuration changes do not overwrite the existing clone. New desired resources continue through the Driver's existing materialization pipeline.

Native, Default, CloneFrom and CloneNative continue using per-Agent temporary hosted clones; Close removes those clones. Merely setting a provider environment variable does not opt into persistence. Explicit SyncProfile still targets the host-selected source and remains independent of WithTools execution cloning. Inspect/ProfileState acquire no hosted claim or gateway.

Example: create Agent A with Dedicated+Tools+ThreadStore; run `a.Thread(key).Run`; close A successfully; create Agent B with the same Driver/source/identity/Tool revision/store; `b.Thread(key).Run` can read the retained local provider session. The regression fixture writes an unpredictable nonce in `projects/<provider-session-id>.jsonl`; resumption succeeds only after reading that file and matching its checkpoint nonce. Both Run and Stream cover this path.

The SDK uses a persistent 0600 owner.lock with an exclusive OS lock, strict versioned ownership records and private directory permissions. It never unlinks/truncates/replaces owner.lock. Supported filesystem classes are local ext4/XFS/tmpfs, APFS/HFS+, NTFS/ReFS; unsupported/remote/unknown filesystems fail closed with `profile.ErrUnsupportedFilesystem`. Unix checks owner/mode/link identity. Windows uses protected current-user/System DACLs, verifies file IDs/reparse points, and holds a no-delete-sharing CreateFile handle with LockFileEx. Native Windows verification belongs to T26.

`profile.ErrUnsafe` reports unverified ownership, paths, permissions or control records. `profile.ErrRecoveryRequired` means a previous active generation did not finish a clean shutdown. Kernel lock release on process exit alone does not prove its provider children exited. A new Agent refuses the dirty generation without changing state, sending a prompt or modifying the Thread store. There is no TTL-based takeover.

Offline recovery: stop all hosts using the namespace; prove and wait for every provider process tree to exit through host process-management records; back up the complete namespace, including transcripts; acquire the same exclusive lock and verify markers/permissions; remove only proven-owned MCP projections using provider path, owner and rendered fingerprint; verify no old gateway remains usable; atomically change active to ready and unlock. If any proof is unavailable, retain active and the backup. Alternatively, once all users have stopped, move the entire namespace to an isolated backup and start a new namespace; this loses local session continuity and uses the existing resume-rejection policy. Never delete only owner.lock. Permanent historical-data deletion belongs to host maintenance after the same stop/backup/exclusive-ownership checks; Agent.Close is not a history deletion API.

Close first closes admission and cancels runs. It preserves the first provider close to unblock cancellation, drains admitted runs, closes late-started writers, removes proven-owned hosted MCP projections, closes the gateway, then deletes temporary clones or releases persistent claims. Any failed phase remains retryable with a fresh context. A cleanup error before OS unlock retains exclusive ownership. After a successful OS unlock, a later handle-close failure retains only the pending cleanup phase: retry never touches the successor Agent's state. User-modified hosted entries are preserved under the existing rendered-fingerprint rule; their ownership records are dropped and gateway shutdown revokes the original token.

Already deleted historical transcripts cannot be reconstructed from resume IDs. Continue-or-start retains its existing single safe resume-rejection fallback; ResumeOnly rejects and does not replace the healthy stored record. The opaque host key is unchanged.

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

Persistent profile ownership uses the existing `golang.org/x/sys` v0.41.0 as a
direct dependency. Its maintained OS lock, file-identity and ACL APIs improve
the verified-handle boundary and stay inside `internal/hostedprofile`; no new
version or public dependency type is introduced.

The hermetic end-to-end test runs a real child process through the Cursor
Driver. That fixture reads the isolated materialized provider MCP profile,
resolves the bearer environment reference, performs MCP discovery/list/call,
emits official provider stream records, and verifies unauthorized access. It
then closes the Agent, forces a different loopback port, reconstructs an Agent
against the same Thread store, resumes a third turn, and proves the source
profile was never polluted. Temporary-profile fixtures verify endpoint and
clone reclamation; Dedicated fixtures require a retained session-file nonce
after clean reconstruction. They make no paid provider call.

Independent lifecycle fixtures also verify exact authenticated old-token
revocation, clean Dedicated reconstruction and bounded cleanup retry. Run
`go test -race -count=10 ./internal/hostedprofile -run TestAlignmentReleaseLifecycle`
to inject failure at the existing private close seams after actual OS unlock:
a successor remains active, its files stay unchanged, and a third claimant
remains excluded. This proves behavior under injected failures on the executing
OS; it does not claim a real handle failure or native Windows ACL/sharing proof.
