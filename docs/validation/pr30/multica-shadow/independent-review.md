# Independent physical review of actual Multica acceptance

Review marker: **便便**. A separate read-only reviewer inspected the local
artifacts, actual workspaces, daemon log and running binary. The reviewer did
not change a verdict, operate services or make another model call.

The [acceptance manifest](acceptance.json) explicitly composes four passed
individual cases. This is **not a single full-suite green run**: run03 retains
three passes and one collection failure; run04 selected only the corrected
restart case and retains the four-case aggregate's incomplete/false verdict.

| Accepted scenario | Original evidence | Independent observations |
|---|---|---|
| Development, explicit mention and actual UI | run03 case 1 | The actual two source files match the host receipt before/after hashes. Three host commands have the fixed argv, task, cwd and matching stdout/stderr hashes. Six fixed cases and four generated tests completed, with no skip/expected failure. Screenshot and API identify the same successful task. |
| Current comment, retry identity and AfterDone | run03 case 2 | Original member/comment/task/agent identities match the unique delivered timeline receipt and actual SDK immediate Ack at 02:36:58.534. The repeated client request ID has one comment and one accepted/delivered pair. Foreign-issue and unknown-target checks pass. The 43-byte output exactly matches its marker. Queued original text/author survive and the final output is AFTERCURRENTDONE. Old SDK cleanup at 02:38:02.153 precedes the new start at 02:38:02.353. |
| Cancel and start over, including partial output | targeted run04 case 3 | Actual old task is cancelled with 21 partial messages, nine tool uses, eight results and observed usage. Its unique formal daemon startup line matches the exact task and all observed tool cwd values. The old file is eight bytes `STARTED\n`; the new successful task's file is seven bytes `NEWGOAL`, with no newline. Old cleanup at 02:48:42.685 precedes new start at 02:48:42.809. Independent SDK/provider identities, normal new cleanup, screenshot and attachment all agree. |
| Chat Steer, priority, withdrawal and isolation | run03 case 4 | The session has exactly four task records. Active is cancelled, priority then ordinary succeed, and withdrawn has no start, messages or SDK log. The other session has no tasks/messages. Actual assistant history and screenshot show priority before ordinary. Cleanup precedes both successor starts, with distinct SDK RunIDs. |

The restart files' independently recomputed SHA256 values are:

- `stopped.txt`: `971fe3d6a96d8ce5b303aab72695067bd4549537ea98fb4601308e37c0626c88`.
- `restarted.txt`: `67c10fc898fb3f31206bb029df8e149f81acc5f4f10b0860b4c88cdc6806c304`.

Both selected executions' ten frozen source hashes match their respective E2E
commits (`0250bea3d` and `25c0ff1b2`). Their actual `/proc/44396/exe` hash is
`65f27dc7094ac36491453a6fac2c7ce02eca203eeabf63871c01953b6b184c34`, with
backend `agent-adaptor`, production source `4f4a0b285` and SDK build `3950473`.
The source difference is restricted to cancelled-task workspace collection,
its negative regressions and documentation. Earlier three passed scenarios did
not need another paid execution.

The native comparison passed its single development/UI case independently;
the three shadow-only cases were explicitly skipped in that phase. Every old
failed report and process result remains unchanged. In particular, a read-only
diagnostic confirmation never converts an original failed aggregate to success.

The [final environment check](environment/run04-final-summary.json) passed all
eleven checks: API/Web healthy, actual daemon identity/hash/backend verified,
no active tasks, no unfinished database tasks, no pending supplements or
terminal reports, and no daemon child processes. Services remain running for
local inspection. API's self-reported build commit remains unknown; daemon
binary provenance is independently verified.

Observed configuration drift explicitly produced `fresh_fallback=true` and a
different provider session. This establishes bounded continuity behavior, not
full provider resume. Model/effort are configured as `gpt-6.1-sol / xhigh`;
incoming-only protocol evidence does not independently attest outgoing effort.
The pilot does not establish durable restart/import, every provider/plugin,
the complete mention grammar, statistical noninferiority or a production rollout.
