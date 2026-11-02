// Package agui translates adaptor requests, events, and approvals to and from
// the AG-UI protocol. It consumes the public Runner, Stream, Event, and Result
// contracts and does not create a separate execution path. If a Driver emits
// no assistant text delta, Events preserves a non-empty final Result.Text as
// one complete assistant message immediately before the terminal event.
// SubagentUpdate lifecycles become AG-UI Activity messages with activityType
// "subagent", allowing clients to render live delegated work outside the
// parent assistant transcript.
//
// Text and reasoning lifecycles open on their first non-empty content.
// Operational/provider Notices are excluded from public projection by default;
// protocol warnings, steps, approvals, drops and subagent activity are retained.
// WithNoticePassthrough opts exact additional Notice kinds into projection.
// Numeric projections accept json.Number after recording/replay. Approval retry
// attempts, latency nanoseconds and exit codes require exact, in-range integers;
// fractional or overflowing values are ignored (retry/latency default to zero).
//
// RunRegistry maps a frontend Thread key to one active execution. Send starts
// Thread.Send when idle and supplements the active Stream otherwise;
// SendActive only supplements and never starts an execution. The run creator
// remains the sole Events consumer and must Forget the stream after draining.
// Hosts that need input beyond one execution can keep a Thread.Standby handle.
//
// MessageUpdate and TurnEnd have no native AG-UI event type and become CUSTOM
// events named "message.update" and "turn.end". The turn payload omits
// provider metadata. Done emits the authoritative RUN_FINISHED or RUN_ERROR,
// closing any remaining message, reasoning, tool and Activity lifecycles.
// Raw process chunks have no AG-UI projection.
package agui
