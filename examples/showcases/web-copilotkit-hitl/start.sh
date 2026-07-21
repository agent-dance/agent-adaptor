#!/usr/bin/env bash
# Start the AG-UI Go backend only (Next.js: see start-all.sh or README).
#
# Usage:
#   ./start.sh           # Codex (default)
#   ./start.sh claude    # Claude Code
#   ./start.sh cursor    # Cursor Agent
#   AGUI_AGENT=cursor ./start.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
if [[ "${1:-}" == "codex" || "${1:-}" == "claude" || "${1:-}" == "cursor" ]]; then
	export AGUI_AGENT="$1"
	shift || true
fi
cd "$ROOT"
BACKEND_BIN="$(mktemp "${TMPDIR:-/tmp}/agent-adaptor-web-copilotkit-hitl.XXXXXX")"
cleanup() { rm -f "$BACKEND_BIN"; }
trap cleanup EXIT
go build -o "$BACKEND_BIN" ./examples/showcases/web-copilotkit-hitl
"$BACKEND_BIN" "$@"
