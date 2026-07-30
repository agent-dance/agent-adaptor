# Documentation map

The public v1 model is Agent, Thread, Stream, Event, Result, and Driver. Start with the current integration documents below; design and implementation records are background material rather than usage references.

## Current integration documents

| Document | Purpose |
|---|---|
| [`../README.md`](../README.md) / [`../README.zh-CN.md`](../README.zh-CN.md) | Product overview, quick start, packages, and examples. |
| [`../AGENTS.md`](../AGENTS.md) | Final architecture boundaries, invariants, and release gates. |
| [`api-reference.md`](./api-reference.md) | Complete public API and option scopes. |
| [`tools.md`](./tools.md) | Provider-neutral host-defined Tools, schemas, errors, lifecycle, security, and Thread compatibility. |
| [`streaming.md`](./streaming.md) | Unified Event consumption, cancellation, backpressure, AG-UI, and SSE. |
| [`streaming-adapter-contract.md`](./streaming-adapter-contract.md) | Streaming and event obligations for Driver authors. |
| [`structured-output.md`](./structured-output.md) | Typed and JSON-schema output. |
| [`a2a.md`](./a2a.md) | A2A bridge, client, delegation, and exposure policy. |
| [`run-policy.md`](./run-policy.md) | Sandbox, feature, approval, timeout, fallback, and retry policy. |
| [`public-errors.md`](./public-errors.md) | Public sentinels, typed errors, and `errors.Is` / `errors.As` guidance. |
| [`profile-resource-provider-matrix.md`](./profile-resource-provider-matrix.md) | Provider support and materialization behavior for profile resources. |
