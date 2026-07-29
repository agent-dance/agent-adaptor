#!/usr/bin/env bash
# Start the real team-agent-workflow backend and the maintained CopilotKit UI.
#
# Usage:
#   ./examples/showcases/team-agent-workflow/start-all.sh claude
#   ./examples/showcases/team-agent-workflow/start-all.sh codex -plan=claude -review=claude
#
# Opening the UI is free. Every submitted chat message starts one leader run
# plus plan, impl, and review role runs against the selected local CLIs.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
COPILOTKIT_WEB="$ROOT/examples/web-chat/copilotkit/web"

usage() {
	printf 'Usage: %s <claude|codex|cursor|codebuddy> [team-agent-workflow flags...]\n' "$0" >&2
}

if [[ $# -eq 0 || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
	usage
	exit 2
fi

LEADER="$1"
shift
case "$LEADER" in
	claude|codex|cursor|codebuddy) ;;
	*)
		usage
		printf 'Unsupported leader: %s\n' "$LEADER" >&2
		exit 2
		;;
esac

command -v go >/dev/null || { printf 'go is required\n' >&2; exit 1; }
command -v node >/dev/null || { printf 'Node.js 20+ is required\n' >&2; exit 1; }
command -v npm >/dev/null || { printf 'npm is required\n' >&2; exit 1; }
command -v curl >/dev/null || { printf 'curl is required\n' >&2; exit 1; }

: "${TEAM_ADDR:=127.0.0.1:8080}"
: "${TEAM_BACKEND_BASE_URL:=http://127.0.0.1:8080}"
: "${TEAM_UI_PORT:=3000}"
: "${TEAM_UI_ORIGIN:=http://127.0.0.1:${TEAM_UI_PORT}}"
: "${TEAM_TIMEOUT:=2h}"
: "${TEAM_ROLE_TIMEOUT:=4m}"
: "${KEEP_WORKSPACE:=0}"

TEAM_BACKEND_BASE_URL="${TEAM_BACKEND_BASE_URL%/}"
export AGENT_BACKEND_URL="$TEAM_BACKEND_BASE_URL/agent"
export NEXT_PUBLIC_AGENT_BACKEND_BASE="$TEAM_BACKEND_BASE_URL"
export NEXT_PUBLIC_COPILOTKIT_MODE=team-agent-workflow
export COPILOTKIT_TELEMETRY_DISABLED=true
export PORT="$TEAM_UI_PORT"

cd "$COPILOTKIT_WEB"
if [[ ! -d node_modules ]]; then
	npm ci
fi
npm run build

BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/team-agent-workflow.XXXXXX")"
BACKEND_PID=""
cleanup() {
	trap - EXIT INT TERM HUP
	if [[ -n "$BACKEND_PID" ]] && kill -0 "$BACKEND_PID" 2>/dev/null; then
		kill "$BACKEND_PID" 2>/dev/null || true
		wait "$BACKEND_PID" 2>/dev/null || true
	fi
	rm -rf -- "$BUILD_DIR"
}
trap cleanup EXIT INT TERM HUP

cd "$ROOT"
go build -o "$BUILD_DIR/team-agent-workflow" ./examples/showcases/team-agent-workflow

BACKEND_ARGS=(
	"-leader=$LEADER"
	-web-mode
	"-web-addr=$TEAM_ADDR"
	"-web-cors=$TEAM_UI_ORIGIN"
	"-timeout=$TEAM_TIMEOUT"
	"-role-timeout=$TEAM_ROLE_TIMEOUT"
)
if [[ "$KEEP_WORKSPACE" == "1" ]]; then
	BACKEND_ARGS+=(-keep-workspace)
fi
BACKEND_ARGS+=("$@")

"$BUILD_DIR/team-agent-workflow" "${BACKEND_ARGS[@]}" &
BACKEND_PID=$!

ready=0
for _ in {1..120}; do
	if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
		wait "$BACKEND_PID" || true
		printf 'team-agent-workflow backend exited before becoming ready\n' >&2
		exit 1
	fi
	if curl -fsS "$TEAM_BACKEND_BASE_URL/health" >/dev/null 2>&1; then
		ready=1
		break
	fi
	sleep 0.25
done
if [[ "$ready" != "1" ]]; then
	printf 'timed out waiting for %s/health\n' "$TEAM_BACKEND_BASE_URL" >&2
	exit 1
fi

printf 'team backend: %s/agent\n' "$TEAM_BACKEND_BASE_URL"
printf 'CopilotKit UI: %s\n' "$TEAM_UI_ORIGIN"
printf 'Submitting one message runs four real, potentially billed CLI turns.\n'

cd "$COPILOTKIT_WEB"
npm run start -- --port "$TEAM_UI_PORT"
