#!/usr/bin/env bash
# One-shot: team-agent-workflow AG-UI backend + Next.js dev frontend (foreground).
#
# The leader is always Claude Code (persistent process); it delegates to the
# curated Codex/Claude A2A roles. --web-mode is injected automatically — without
# it the backend runs the workflow once on the CLI and exits instead of serving
# the frontend.
#
# The frontend is the CopilotKit web app shipped with the sibling
# web-copilotkit-hitl example; it targets http://localhost:8080/agent by default.
#
# Usage:
#   ./start-all.sh                       # backend on :8080 + frontend on :3000
#   ./start-all.sh --web-addr :9090      # extra flags are passed to the backend
#   ./start-all.sh --claude-model <name> # e.g. override the leader model
#
# Open http://localhost:3000 once both are up. Ctrl-C stops both.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
WEB_DIR="$ROOT/examples/showcases/web-copilotkit-hitl/web"

cd "$ROOT"
BACKEND_BIN="$(mktemp "${TMPDIR:-/tmp}/agent-adaptor-team-agent-workflow.XXXXXX")"
BACKEND_PID=""
cleanup() {
	if [[ -n "$BACKEND_PID" ]]; then
		kill "$BACKEND_PID" 2>/dev/null || true
		wait "$BACKEND_PID" 2>/dev/null || true
	fi
	rm -f "$BACKEND_BIN"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

go build -o "$BACKEND_BIN" ./examples/showcases/team-agent-workflow
"$BACKEND_BIN" --web-mode "$@" &
BACKEND_PID=$!

cd "$WEB_DIR"
if [[ ! -d node_modules ]]; then
	npm ci
fi
# Prefill the chat input so the user just hits send to see the demo, and declare
# on the page what this demo shows. The Next dev server reads NEXT_PUBLIC_* from
# the environment at startup.
export NEXT_PUBLIC_DEFAULT_PROMPT="${NEXT_PUBLIC_DEFAULT_PROMPT:-start}"
export NEXT_PUBLIC_DEMO_TITLE="${NEXT_PUBLIC_DEMO_TITLE:-Team agent over multiple agent bases + structured output}"
export NEXT_PUBLIC_DEMO_DESC="${NEXT_PUBLIC_DEMO_DESC:-A Claude Code leader orchestrates a team whose roles run on different bases (Codex + Claude Code) via one MCP delegation tool.
The plan stage (Codex) returns a schema-validated coding plan — shown as a downloadable attachment on its subagent card.
Watch the Subagents panel: plan (Codex) → impl (Claude Code) → review (Codex).}"
npm run dev
