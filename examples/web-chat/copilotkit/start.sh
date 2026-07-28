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
: "${ADDR:=127.0.0.1:8080}"
: "${CORS_ORIGIN:=http://localhost:3000}"
export ADDR CORS_ORIGIN
if [[ "${1:-}" == "codex" || "${1:-}" == "claude" || "${1:-}" == "cursor" ]]; then
	export AGUI_AGENT="$1"
	shift || true
fi
cd "$ROOT"
exec go run ./examples/web-chat/copilotkit "$@"
