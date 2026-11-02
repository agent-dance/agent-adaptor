# Documentation map

The v2 public model is **Agent, Thread, Stream, Event, Result, and Driver**. `Send` starts a task; `Stream.Send` adds input; `Thread.Standby` keeps a conversation accepting messages. SDK imports use `github.com/agent-dance/agent-adaptor/v2`.

Start with the integration documents below. Design and implementation records explain rationale; current code, godoc, and the frozen root API define behavior.

## Current integration documents

| Document | Purpose |
|---|---|
| [English README](../README.md) | Overview, quick start, messages, Standby, packages, examples, and v1 → v2 migration. |
| [API reference](./api-reference.md) | Root API, Send/Stream/Standby/ThreadRunner, option scopes, events, results, approvals, errors, and extension hooks. |
| [Streaming guide](./streaming.md) | Events/Done, backpressure, message delivery and Driver matrix, Cursor relay, Standby, AG-UI, and SSE. |
| [Run policy](./run-policy.md) | Sandbox/features, Policy.Messages, approval modes, immediate fallback, timeout, rejection, and retry. |
| [Structured output](./structured-output.md) | RunAs, Send with a schema, automatic native/prompt negotiation, validation, and Decode. |
| [Host-defined Tools](./tools.md) | Typed handlers, schemas, error sanitization, lifecycle, cancellation, and named-Thread revision compatibility. |
| [Public errors](./public-errors.md) | RunError, MessageError, busy/finished/unsupported categories, delivery failure reasons, and errors.Is/As. |
| [Examples](../examples/README.md) | What each executable currently does, live-call requirements, and smoke commands. |
| [Driver streaming contract](./streaming-adapter-contract.md) | Driver event obligations; read alongside the current Driver godoc and frozen [message SPI](../driver/messages.go). |
| [Teams](./team.md) | Teams, local/remote members, message routing, limits, durable recovery and Fork. |
| [A2A integration](./a2a.md) | A2A bridge, client, Remote members, and exposure policy. |
| [Profile resource matrix](./profile-resource-provider-matrix.md) | Provider support and resource materialization. |
| [Architecture contract](../AGENTS.md), [approved v2 proposal](./v2-steer-team-proposal.md) | Architecture boundaries, release gates and design rationale. When a document and the code differ, current code and [root API golden](../testdata/root_api.golden) are authoritative. |

Translations: [简体中文](../README.zh-CN.md), [日本語](../README.ja.md), [한국어](../README.ko.md), [Deutsch](../README.de.md).
