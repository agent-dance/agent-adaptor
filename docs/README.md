# Documentation map

The public v1 model is Agent, Thread, Stream, Event, Result, and Driver. Start with the current integration documents below; design and implementation records are background material rather than usage references.

## Current integration documents

| Document | Purpose |
|---|---|
| [`../README.md`](../README.md) / [`../README.zh-CN.md`](../README.zh-CN.md) | Product overview, quick start, packages, and examples. |
| [`../AGENTS.md`](../AGENTS.md) | Final architecture boundaries, invariants, and release gates. |
| [`api-reference.md`](./api-reference.md) | Complete public API and option scopes. |
| [`usage-guide.md`](./usage-guide.md) | Agents, Threads, resources, profiles, runtime services, and host integration. |
| [`streaming.md`](./streaming.md) | Unified Event consumption, cancellation, backpressure, AG-UI, and SSE. |
| [`streaming-adapter-contract.md`](./streaming-adapter-contract.md) | Streaming and event obligations for Driver authors. |
| [`structured-output.md`](./structured-output.md) | Typed and JSON-schema output. |
| [`a2a.md`](./a2a.md) | A2A bridge, client, delegation, and exposure policy. |
| [`run-policy.md`](./run-policy.md) | Sandbox, feature, approval, timeout, fallback, and retry policy. |
| [`public-errors.md`](./public-errors.md) | Public sentinels, typed errors, and `errors.Is` / `errors.As` guidance. |
| [`migrating-to-v1.md`](./migrating-to-v1.md) | Complete migration from the `v0.12.0` public baseline. |
| [`profile-resource-provider-matrix.md`](./profile-resource-provider-matrix.md) | Provider support and materialization behavior for profile resources. |

## Architecture and execution records

These documents explain why v1 has its current shape and how the cutover was executed. When an implementation record differs from the public API, the current integration documents and code are authoritative.

| Document | Status |
|---|---|
| [`api-v1-redesign.md`](./api-v1-redesign.md) | Approved public design and scenario analysis. |
| [`v1-takeover-audit.md`](./v1-takeover-audit.md) | Takeover findings, correctness blockers, and closure evidence. |
| [`api-v1-implementation-plan.md`](./api-v1-implementation-plan.md) | Phase plan and decision log. |
| [`p5.2-recon.md`](./p5.2-recon.md) | Mechanical cutover inventory and execution record. |

## Historical material

Files named `workstream-*.md`, the v0.5 plans and release notes, early inventory and option-decision records, and superseded resolver or roadmap drafts are historical design evidence. They may describe APIs or package layouts that no longer exist and must not be used as integration instructions.

## Maintenance rules

- README files, current integration documents, and examples may reference only exported, existing APIs and paths.
- Public behavior changes require matching godoc, contract tests, API reference, usage guidance, migration notes, and changelog entries.
- Examples use final package and directory names; removed aliases and temporary suffixes are not documented.
- Thread keys are single opaque host-owned strings. Provider resume identifiers are checkpoint details, not a second consumer identity.
- Driver capability and profile-resource matrices must report implemented behavior truthfully; unsupported probes and resources are never presented as successful.
- Event consumers must drain the unified channel or cancel the Stream before abandoning it.
