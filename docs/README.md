# Documentation map

The public v1 model is Agent, Thread, Stream, Event, Result, and Driver. Start with the current integration documents below; design and implementation records are background material rather than usage references.

## Current integration documents

| Document | Purpose |
|---|---|
| [`../README.md`](../README.md), and its [`zh-CN`](../README.zh-CN.md), [`ja`](../README.ja.md), [`ko`](../README.ko.md), [`de`](../README.de.md) translations | Product overview, quick start, packages, and examples. |
| [`../AGENTS.md`](../AGENTS.md) | Final architecture boundaries, invariants, and release gates. |
| [`api-reference.md`](./api-reference.md) | Complete public API and option scopes. |
| [`tools.md`](./tools.md) | Provider-neutral host-defined Tools, schemas, errors, lifecycle, security, and Thread compatibility. |
| [`streaming.md`](./streaming.md) | Unified Events, actual provider observation support, scoped recording, bound delegation facts, replay, AG-UI and SSE. |
| [`streaming-adapter-contract.md`](./streaming-adapter-contract.md) | Streaming and event obligations for Driver authors. |
| [`structured-output.md`](./structured-output.md) | Typed and JSON-schema output. |
| [`a2a.md`](./a2a.md) | A2A bridge/client, closed failure controls, delegation binding/relay, and exposure policy. |
| [`run-policy.md`](./run-policy.md) | Sandbox, approval, independent run/delegation budgets, wall-clock limits, fallback and retry policy. |
| [`public-errors.md`](./public-errors.md) | Public sentinels, typed errors, and `errors.Is` / `errors.As` guidance. |
| [`profile-resource-provider-matrix.md`](./profile-resource-provider-matrix.md) | Provider support and materialization behavior for profile resources. |

## Implementation planning

| Document | Purpose |
|---|---|
| [`internal-history-alignment-plan-2026-09-07.md`](./internal-history-alignment-plan-2026-09-07.md) | Commit-by-commit comparison with the internal repository and a v1-compatible alignment plan, based on the branches fetched on September 7, 2026. |
| [Alignment task dispatch package](./alignment-tasks/2026-09-07/README.md) | Seven gated batches with 45 task.json files, independent ownership, 96 traceable requirements, and dispatch validation. |

2026-09-07 internal 对齐的生产改动已通过 B04；B05 的独立跨层验证、Driver 一致性、CI、文档示例与 R021 常驻回调修复和 R022 接收同步验证由 G05 在合流提交上验收。原生 Linux 曾发现 CodeBuddy 常驻轮次交接 race，旧 G05 证据保留，所有 B06 验收必须使用修复后的新冻结提交。冻结规范见 [合同清单](alignment-tasks/2026-09-07/contracts/frozen.json)，无需 CLI 的使用入口见 [offline 示例](../examples/offline/main.go)。T25–T30 必须使用 G05 冻结的同一提交补齐原生平台与真实 provider 证据；本页和 CI 配置不代替实际验收。

## Contributor validation

The [Go workflow](../.github/workflows/go.yml) keeps paid/live gates closed for
automatic checks and defines native platform, race, repeated and fuzz jobs.
The [B06 command list](./alignment-tasks/2026-09-07/handoffs/T23/b06-commands.md)
and [entrypoint inventory](./alignment-tasks/2026-09-07/handoffs/T23/b06-inventory.json)
give the exact required selections and isolated-profile prerequisites. Execute
them on the same G05 frozen commit; configuration, test listing and cross-compiling
do not prove native-platform or live-provider success. Real provider execution
requires its separate authorization and both gates.
