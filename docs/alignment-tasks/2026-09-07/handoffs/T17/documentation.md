# T17 / W09-R09, W11-R08, W12-R07

Cursor now publishes formal MCP and custom Subagent capability lifecycles into
its existing Event stream. It compares provider references to the resolved
catalog exactly, preserves canonical keys, rejects ambiguous or unknown names,
deduplicates repeated starts/terminals, requires an explicit formal result for
success/failure, and ends pending calls as cancelled/interrupted/protocol-error
without manufacturing successful calls. Arguments, Raw, URLs, headers and error
text never enter the new safe projection; original Raw, Transcript and terminal
payload remain available. Tracking and replay state is limited to 4096 calls;
limits or unknown facts produce a safe once-per-reason runtime notice.

Both Request.Streaming values execute the actual existing print stream-json
transport, so both Descriptor Observation branches declare MCP/Subagents.
Neither branch implies an implemented terminal JSON batch path. Skill resource
synchronization remains available; Skills observation and Todos are false.
No slash prompt, text checklist, default agent name, path, guessed parent, or
unknown result is interpreted as proof. Usage remains nil because the supported
print protocol does not establish numeric token usage; arbitrary usage fields
remain in Raw. Strong session/terminal checkpoint guards remain intact.

Nonempty append is rejected in both public preflight and direct Driver.Run with
SystemPromptUnsupportedError / unsupported_driver, before materialization or
launch. Empty call override clears a nonempty construction default and sends
the original prompt unchanged. Invalid UTF-8/NUL keep the shared validation
precedence. Cursor does not change to ACP, add persistence/native schema/fork or
interactive approvals, or redirect append into instructions.

Reproducible consumer example:

```go
agent := adaptor.New(cursor.Driver(cursor.Config{}),
    adaptor.WithAppendSystemPrompt("unsupported"))
_, err := agent.Run(ctx, "hello") // errors.Is(err, adaptor.ErrSystemPromptUnsupported)
result, err := agent.Run(ctx, "hello", adaptor.WithAppendSystemPrompt(""))
```

Merge targets owned by G04:

- docs/streaming.md: Cursor print observation matrix, exact catalog/result rules,
  absent fact meaning, safe notices, unsupported Skills/Todo and nil Usage.
- docs/api-reference.md and docs/run-policy.md: Cursor append unsupported in
  direct/public preflight; empty override retains existing prompt semantics.
- CHANGELOG.md: Cursor MCP/Subagent facts, rejection before resource creation,
  and corrected old conformance live gate/isolation.
- README.md and translated driver matrices: only capability observation changes;
  no new root API or With* name. Local cursor/doc.go and README-streaming.md
  already contain the detailed contract and live invocation.

No new public declaration, alias, dependency or golden update is required.
New code uses the already accepted driver/capability types and neutral tracker;
standard library JSON and bounded maps remain localized within cursor/.

Sources were read through fixed internal Git objects, especially
1921636510ced4c830c0287fe68d41218f1ed185 and
 e2f0620bdd6477e6fe16f6db5648093589342ca2:cursor/capability_observation_test.go
and cursor/parser.go. The supported wrappers also follow Cursor's official
https://cursor.com/docs/cli/reference/output-format page (read 2026-09-07).
These are fixture/source evidence, not current live certification of particular
MCP/custom-agent fields. Rejected internal behaviors include broad event aliases,
slash-prompt Skill inference, nonempty/unknown results treated as success,
trimmed identifiers, and checkpoint validity based only on any observed session.

Live test inventory for T23/T30: TestAlignmentLiveCursorPrintResume,
TestAlignmentLiveCursorCapabilities, TestAlignmentLiveCursorCancelPartial,
TestAlignmentLiveCursorUnsupportedAppend; existing TestCursorDriverConformance
is also corrected. All require cursor_live plus AGENT_ADAPTOR_LIVE_CONFORMANCE=1.
They allocate temporary HOME/USERPROFILE/CURSOR_HOME/workspace, record CLI
--version, and require runner-provided API-key authentication. Optional settings
are AGENT_ADAPTOR_CURSOR_COMMAND and AGENT_ADAPTOR_CURSOR_MODEL. Required supported
probes fail when CLI/evidence is missing. Ordinary disabled live probes and
unimplemented StreamSupport/native schema/SPI fork probes are explicitly
inapplicable and do not count as live acceptance.

This task executes macOS hermetic fixtures and a live-tag build with the live
environment gate closed. No actual CLI/version/auth/profile probe, paid call,
native Windows or Linux test is claimed. B06 retains those gates on the final
frozen implementation SHA. Final command results and real source SHA are recorded
outside the referenced commit in result.json and evidence/.

Attempt 2 closes independent finding T17-F01: when `toolName` was present but
numeric, null or empty, a valid `name` incorrectly hid the malformed field.
Both recognized operation aliases now independently require valid, nonempty
strings before equality checking. A malformed alias yields a safe
invalid_reference notice and no observed call; a malformed completion cannot
complete an already started call. Unrelated additive fields, Raw, Transcript,
terminal and healthy checkpoint behavior remain unchanged. The owned regression
covers both actual Driver.Run print branches, positive alias controls, symmetric
invalid fields, and a pending terminal. G04 should merge this clarification into
the Cursor observation subsection of docs/streaming.md and the same CHANGELOG
entry. No public declaration, dependency, scope or golden changes were needed.

R017 / attempt 3 adds TestAlignmentLiveCursorDedicatedToolsColdResume in
cursor/alignment_live_cold_resume_test.go. It uses two public Agents, the same
memory Store, Dedicated source, workspace, identity, Tool definition/revision
and Thread key. A actually invokes the tool with a random nonce, saves a
healthy provider checkpoint, and completes a bounded Close. B is constructed
after that Close and calls Thread(key, ResumeOnly()) with a nonce-free prompt;
its tool response is a fixed acknowledgement without access to historical
nonce data. B must recall the nonce and actually use its new tool gateway while
preserving provider session ID, store record and compatibility fingerprint.

The test-only configured Driver wrapper forwards every real invocation without
changing responses, events, transport or checkpoints. It captures resolved
profile/MCP/session inputs only to check the retained hosted profile and original
resume selector. The test identifies actual nonempty provider files under an
exact session-ID directory in the isolated effective profile, hashes them before
and after A.Close and immediately before B dispatch, and requires the files to
remain present after B. Unknown or ambiguous file layouts fail the required live
probe. It never creates session files or uses a test marker as session evidence.
The original empty Dedicated source must remain unchanged. Gateway revocation,
released listener, rotated endpoint/bearer/carrier, and rejection of the old
bearer by the new gateway are asserted without logging credentials. Both Agents
receive a bounded Close, including failure cleanup. Cursor continues to spawn
per turn and gains no persistent/fork/Skills/Todos capability.

G04 should add this scenario to the existing docs/streaming.md live inventory
and CHANGELOG verification note; cursor/README-streaming.md carries its local
contract. Existing print, MCP/Subagent, unsupported and cancellation live tests
remain intact. This is fixture coverage only: gated compilation/canary checks
do not certify a real session, tool invocation or provider version. No live,
paid call, user-profile read, native Windows/Linux run or production change is
part of attempt 3. B06 must run the new required scenario under both explicit
live gates on the final implementation SHA. Pre-R017 reports/evidence remain
archived in the untracked handoff evidence tree.
