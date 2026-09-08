# T36 — Codex RPC shutdown coordination

## Behavior and concrete reproduction

A Codex app-server response can already be decoded while the process owner closes its Client during cancellation. The pinned jsonrpc2 v0.2.1 external Conn.Close closes pending channels while retaining their map entries; releasing that decoded response then causes the original reader to send on a closed channel. The preserved baseline fixture stops the real dependency reader at ObjectStream.ReadObject return, invokes Client.Close, observes the pending call end, and releases the response. It reproduces the native Linux panic on the unmodified G04 Client. No replacement JSON-RPC implementation, recovery, dependency modification, delay, or protocol relaxation is used.

Client.Close now closes admission, synchronously cancels this Client's active operations, and closes its owned ObjectStream once. It never calls Conn.Close or joins the reader. Only the original synchronous reader settles pending response channels after EOF/read error. All seven outbound typed entries share private lifecycle coordination; server-request rejection retains the same coordination and explicit method-not-found response. Notification delivery stays synchronous and in wire order. An early server-request uses the already initialized connection passed to the handler, because NewConn may start its reader before NewClient publishes its conn field; a real-reader initialization-stage regression covers this ordering.

The private outbound gate covers only DispatchCall, Notify, and server ReplyWithError sending. Waiting for a response never holds that gate, and Close never takes it. A queued operation can stop on caller cancellation or Client.Close while another write is blocked. Each send has its own writeAttempt; if the pinned sender changes a real WriteObject error to ErrClosed after concurrent EOF, the exact original error is retained for that operation. An unwritten operation never inherits another operation's write failure. No request ID, JSON field, or provider meaning is inferred by this helper.

Caller cancellation/deadline and custom causes remain visible with errors.Is; official RPC errors remain visible with errors.As. A private shutdown cause distinguishes Client.Close from a caller deliberately supplying jsonrpc2.ErrClosed as its cancellation cause. Client-only shutdown remains IsDisconnected without becoming caller cancellation. First established caller cancellation is preserved; a later caller cancellation cannot relabel a completed Close. No exported declarations or API/golden changes are needed.

## Boundaries preserved

Logical Client.Close, RPC reader disconnect, stdio ReadDone, stdout/stderr drain, and OS Wait remain separate phases. Close cannot manufacture drain or exit evidence. codec.go, process.go, run.go, parser/terminal/Usage/checkpoint logic, dependencies, generated Go/schema JSON, and root APIs remain unchanged. The existing process owner still performs bounded grace/kill/Wait and captures complete available Raw and partial Response; failed or cancelled turns do not gain a healthy checkpoint or replay a delivered prompt. Healthy resident turns do not wait for process exit.

The new TestAlignmentClientClose uses the actual pinned dependency with bounded ReadObject and writer barriers. It covers close-before-response and response-before-close, original RPC errors, active calls and all new typed calls after Close, caller cancellation/deadline and close ordering, EOF/malformed read, synchronous handler Close and FIFO, server-request rejection, concurrent exactly-once resource release, blocked write release and original write cause, EOF-before-write-error, and queued RPC/notification/server-reply cancellation without cause contamination. Each test releases its gates on cleanup, joins its RPC/closer workers, and observes reader EOF/disconnect; active cancellation registrations are empty after completed operations. It does not infer global goroutine counts or use time sleeps as readiness evidence.

## Central documentation integration (G05)

- Keep the local appserver package godoc paragraph on Client.Close and distinct drain/Wait phases.
- In usage documentation for Agent.Close, retain idempotent bounded resident cleanup, ErrAgentClosed admission, failure partial Result retrieval, and unchanged checkpoint requirements. Mention the corrected Codex response/Close race where relevant; there is no new consumer option.
- Add a CHANGELOG fix entry: Codex app-server shutdown no longer races decoded RPC responses; it releases pending/queued calls without losing original operation errors, notification FIFO, or process output cleanup.
- Do not adopt the historical internal cancellation-to-valid-checkpoint behavior. A known thread ID and delivered prompt do not prove a healthy resumable terminal.

## Validation boundary

The delivery records the final source SHA, exact commands, Go/OS versions, private HOME and provider defaults, external watchdogs, logs and counts in untracked evidence/result. Required commands are complete Codex count1, complete Alignment race count5, new ClientClose race count20, and Codex vet. All paid live/E2E/golden gates are zero; provider CLIs and user credentials are not invoked.

This worktree starts at accepted G04 b2035bc and does not contain same-batch T34. It cannot claim to have executed TestAlignmentCodexStderrAdmission locally. G05 must preserve accepted T34 unchanged and execute its receipt/cancel cases on the combined source; T25 must rerun all original native Linux commands on that replacement SHA. T29 retains its independent authorized live obligation. T36 owner delivery is not G05, T25, T29 or release acceptance.
