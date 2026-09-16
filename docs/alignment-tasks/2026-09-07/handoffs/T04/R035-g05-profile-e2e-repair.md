# T04 — R035 / G05-F02 temporary profile E2E repair

Base: `4290cb9237a89d0a3cbe2ce8dc0c4793dcc6d54b`.
Scope: only `e2e/tools_test.go` and this handoff. No production, public API,
provider guard, canonical task/gate, centralized documentation or dependency changes.

The old real-process fixture required both a cross-Agent resume and zero prior
provider turns after the temporary profile had been deleted. The current Cursor
actual-root guard correctly rejects that stored checkpoint. The original fixture
fails on the prepared base under the same private HOME/USERPROFILE/XDG collector
environment as G05; that red evidence is preserved externally.

The temporary case now first uses public `ResumeOnly` and requires
`errors.Is(err, adaptor.ErrResumeRejected)`, no new child entry, no provider/tool
work, no Finalize call, and identical healthy record and public checkpoint.
Normal continue-or-start then starts exactly one fresh child. The fixture's
provider session identity is derived from its isolated directory, so a new
profile cannot masquerade as the previous conversation. A transparent wrapper
around the real memory store observes one atomic Finalize request and verifies
save/archive/rebind, with the old healthy record intact immediately before the
commit and its prior checkpoint retained in the archived record afterward.

Dedicated still reads its two actual prior turns after Close, resumes the same
provider and store identity, and keeps its provider file after the second Close.
Both selections keep two same-Agent turns, MCP discovery/list/call/auth, gateway
closure and endpoint/token/carrier rotation, old-token 401, source-marker and
source-tree nonpollution, and profile cleanup checks. Raw stdout, exact stderr,
formal terminal and both tool-call/tool-result Transcript entries are asserted
for every successful turn. TestMain logs all real child entries before any
provider operation, including entries that subsequently fail.

Validation uses official Go 1.27.1 with offline caches and all live/golden gates
closed. Each check gets private HOME/USERPROFILE/TMP/XDG directories without
inherited provider environment. Required commands remain unchanged except JSON
logging:

- `go test -json -count=1 . ./profile ./internal/skillruntime ./internal/mcpruntime ./internal/hostedprofile`
- `go test -json -race -count=5 . -run TestAlignmentProfile`
- `go test -json -count=1 ./e2e`
- Focused real-process E2E under the G05 environment, ordinary and race.

Final committed-SHA commands, per-test outcomes, source before/after snapshots,
raw-log SHA256 and the retained base failure are collected in external
`docs/alignment-execution/2026-09-16/live-repair/handoffs/T04/g05-profile-e2e-repair/`.
The ordinary helper skip in `internal/hostedprofile` is the subprocess entry:
parent tests actually execute it with the required helper environment. This
fixture uses Go children and a loopback MCP gateway, no real model/provider.
Local passes do not certify the replacement G05 or native/live gates.
