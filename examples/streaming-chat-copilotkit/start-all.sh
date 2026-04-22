#!/usr/bin/env bash
# One-shot: Go AG-UI backend + Next.js dev server (foreground).
#
# Usage:
#   ./start-all.sh           # Codex
#   ./start-all.sh claude    # Claude Code
#   ./start-all.sh mock      # Interactive HITL mock adapter (recommended)
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
if [[ "${1:-}" == "codex" || "${1:-}" == "claude" || "${1:-}" == "mock" ]]; then
	export AGUI_AGENT="$1"
	shift || true
fi
cd "$ROOT"
go run ./examples/streaming-chat-copilotkit &
BACKEND_PID=$!
cleanup() { kill "$BACKEND_PID" 2>/dev/null || true; }
trap cleanup EXIT INT TERM
cd "$DIR/web"
if [[ ! -d node_modules ]]; then
	npm install
fi
exec npm run dev
