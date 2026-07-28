#!/usr/bin/env bash
# One-shot: Go AG-UI backend + Next.js dev server (foreground).
#
# Usage:
#   ./start-all.sh           # Codex
#   ./start-all.sh claude    # Claude Code
#   ./start-all.sh cursor    # Cursor Agent
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
: "${ADDR:=127.0.0.1:8080}"
: "${CORS_ORIGIN:=http://localhost:3000}"
export ADDR CORS_ORIGIN
if [[ "${1:-}" == "codex" || "${1:-}" == "claude" || "${1:-}" == "cursor" ]]; then
	export AGUI_AGENT="$1"
	shift || true
fi
cd "$ROOT"
go run ./examples/web-chat/copilotkit &
BACKEND_PID=$!
cleanup() { kill "$BACKEND_PID" 2>/dev/null || true; }
trap cleanup EXIT INT TERM
cd "$DIR/web"
if [[ ! -d node_modules ]]; then
	npm install
fi
exec npm run dev
