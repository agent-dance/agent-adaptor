# T17 — R030/R031 Cursor directory and live-oracle repair

Prepared base: `68c30cf445729a9ab94b1b5ad7b50c1f5877579e`.
This handoff covers only Cursor-owned production, fixtures and documentation.
The public API, shared core, process lifecycle and observation capability matrix
are unchanged. Final integration acceptance belongs to the root/T30 gates.

## Findings and repairs

- **T30-F01:** installed Cursor `2026.07.23-e383d2b` ignores SDK `CURSOR_HOME`.
  Configuration uses `CURSOR_CONFIG_DIR` → `XDG_CONFIG_HOME/cursor` →
  `HOME/.cursor`; project data uses `CURSOR_DATA_DIR` → `HOME/.cursor`;
  user extensibility loads from actual `HOME/.cursor`. The installed
  `cursor-config` module exports `WI=G`, so print/resume SQLite lives in the
  **config** root at `chats/md5(path.resolve(cwd))/sessionID/store.db`, while
  project transcripts live in the separate data root. Legacy SDK selection,
  Dedicated and Clone now map to the official roots, with native split roots
  preserved for Native. AuthNone native clone obtains recognized static config
  from the config root and resources from the resource root; auth/history are
  not imported. `--data-dir` and `--plugin-dir` cannot override resolved roots.
- A selected profile now delivers MCP/skills/hooks through a per-run private
  native HOME projection and agents through a fixed-name agents-only plugin.
  Official native MCP environment expansion is preserved byte-for-byte;
  plugin MCP's incompatible `${env:...}` behavior is not used. Actual qualified
  `mcpToolCall.args.name = serverIdentifier + "-" + toolName` is accepted only
  as exact redundant evidence; server/operation remain independently resolved.
  Wrong server, differing aliases, wrong types, empty/null names and unknown
  catalog entries still cannot become successful observations.
- **T30-U01:** a fresh private workspace requires an explicit trust choice.
  Live fixtures use `--trust`; production policy never gains a global force
  override. A separate live control requires the original trust refusal.
- **T30-F03:** Cursor print assistant items reach the existing public
  `NoticeTranscriptItem` contract, not a promised TextDelta stream. The cancel
  fixture enables official `--stream-partial-output` and cancels only on actual
  same-RunID nonempty assistant text. It requires the cancellation reason and
  `errors.Is(context.Canceled)`, real partial Raw/Text/Transcript, no successful
  terminal, unchanged prior healthy checkpoint and bounded teardown/Close.
- **T30-F04:** official opaque `call_id` values contain LF. Full unsafe/reserved
  ID values use a collision-free reserved-domain URL-base64 encoding; ordinary
  safe IDs keep their spelling. Transcript/tool and capability IDs agree,
  and Raw retains original bytes. IDs are never split to infer identity.
- The old R017 file probe found two session-named directories (chat store and
  transcript audit copy). Its failed first-turn-only diagnostic remains in
  evidence. The corrected oracle addresses the official exact SQLite path;
  unknown layouts, empty files and links fail. Original two-Agent/Thread,
  historical nonce, real tool, session identity/file preservation, old gateway
  revocation, credential rotation and bounded Close assertions remain intact.

## Ownership, limits and compatibility

Detailed directory table/layout: `cursor/README-streaming.md`.
Private HOME parent plus marker publish atomically without replacement; each
run has its own `cursor-run-*`. Startup/cancel/normal cleanup never remove
persistent session sources or another invocation. Native retains its HOME and
only projects agents. No new lock, persistent process or second pipeline exists.

Copy is bounded by 4096 entries, 64 levels, 8 MiB per file and 32 MiB total.
Cleanup is rooted, identity/marker verified and limited to 16384 entries,
64 levels and a 15-second cancellation deadline. Directory enumeration uses
64-entry batches before processing. Unknown links/special nodes fail; only
resolved top-level managed skill links can be copied. Ownership, preparation,
I/O and cleanup failures remain observable. Cleanup failure after execution
retains Response/Raw/Transcript/terminal but removes the checkpoint.

Windows creates every projection file/directory with the existing repository's
protected SID/DACL pattern through `NtCreateFile`, verifies owner/DACL/type by
handle before writes, and rechecks reusable roots. It does not infer privacy
from chmod or a selected profile's inheritable ACL. Exclusive publication uses
`NtSetInformationFile(FileRenameInformation, ReplaceIfExists=0)` under the held
parent, not `Root.Rename`. Native Windows execution remains a required Actions
gate; local cross-compilation is only compile evidence.

Stable guards include config/data/resource source paths, canonical config
(excluding only formal `authInfo`; unknown fields and caches remain guarded),
and sorted agents/hooks source paths/content/modes. The static source snapshot
is taken for the invocation before projection; subsequent external edits do not
silently bless a new source. Runtime services cannot redirect roots. Random
HOME/plugin paths and dynamic hosted MCP credentials do not enter guards.
Old checkpoints missing any new proof reject before launch with
ErrResumeRejected; ResumeOnly preserves old healthy state, while normal
continue-or-start retains its existing one safe fresh fallback. AuthNone
clone's recognized-static-field allowlist is separate from the broader guard.

## Review and validation evidence

External evidence directory:
`docs/alignment-execution/2026-09-16/live-repair/handoffs/T17`.
The installed CLI source was read only; operator config/profile files were not
read. `official-cli-resource-proof.json` and
`official-cli-storage-config-proof.json` record exact installed file hashes and
loader/schema/state snippets. Real runs used only official CLI, `gpt-5.2`, fresh
private HOME/profile/workspace, access token from the approved Keychain entry
kept in memory, and `AGENT_CLI_CREDENTIAL_STORE=memory`.

Bounded diagnostic passes before final source freeze:

- Capabilities: 45.46 s; CancelPartial: 50.52 s;
  WorkspaceTrustRequired: 4.06 s (`focused-live-diagnostic.json`).
- DedicatedResourceIsolation: 81.90 s. Native MCP/skill access is a real positive
  control; Dedicated selected MCP/skill/subagent succeeds without new contact
  to the still-live native MCP or native-only token disclosure
  (`isolation-cold-live-diagnostic.json`). The same artifact also retains the
  old ambiguous-file-oracle cold fixture failure before its second turn.
- DedicatedToolsColdResume: 60.62 s, both real turns and all resource rotation
  checks (`cold-live-formal-store-diagnostic.json`).

Ordinary final checks use root `run_go.py`, Go 1.27.1, task-private HOME and
USERPROFILE, removed provider/XDG overrides, the authorized shared caches and
all three paid/golden gates closed. Exact committed-SHA commands, outcomes and
counts are in the external `rework_attempt` report; final integration/live and
Windows/Linux native acceptance are not claimed by these local passes.

Independent review found and regression-tested concurrent first publication,
Native runtime HOME redirection, legacy-empty selection, replacement marker,
exclusive directory publication and Windows ACL boundaries. T04's final
independent report is authoritative for its own review, not this handoff.
