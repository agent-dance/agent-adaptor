#!/usr/bin/env bash
# Start the AG-UI Go backend only (Next.js: see start-all.sh or README).
#
# Usage:
#   ./start.sh           # Codex (default)
#   ./start.sh claude    # Claude Code
#   ./start.sh mock      # Interactive HITL mock adapter (recommended for the demo)
#   AGUI_AGENT=mock ./start.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [[ "${1:-}" == "codex" || "${1:-}" == "claude" || "${1:-}" == "mock" ]]; then
	export AGUI_AGENT="$1"
	shift || true
fi
cd "$ROOT"
exec go run ./examples/streaming-chat-copilotkit "$@"
