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
package agui
