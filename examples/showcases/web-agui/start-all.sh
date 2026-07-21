#!/usr/bin/env bash
# One-shot: Go AG-UI backend + Vite dev server (foreground).
#
# Usage:
#   ./start-all.sh           # Codex
#   ./start-all.sh claude    # Claude Code
#   ./start-all.sh cursor    # Cursor Agent
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
if [[ "${1:-}" == "codex" || "${1:-}" == "claude" || "${1:-}" == "cursor" ]]; then
	export AGUI_AGENT="$1"
	shift || true
fi
cd "$ROOT"
BACKEND_BIN="$(mktemp "${TMPDIR:-/tmp}/agent-adaptor-web-agui.XXXXXX")"
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
go build -o "$BACKEND_BIN" ./examples/showcases/web-agui
"$BACKEND_BIN" &
BACKEND_PID=$!
cd "$DIR/web"
if [[ ! -d node_modules ]]; then
	npm ci
fi
npm run dev
