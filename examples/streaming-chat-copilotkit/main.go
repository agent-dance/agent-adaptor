// streaming-chat-copilotkit is a demo that combines the agent-adaptor SDK's
// streaming + HITL v2 backend with CopilotKit's React UI through the AG-UI
// protocol.
//
// What this example shows:
//
//  1. Standard AG-UI streaming (text / tool_call / reasoning)
//  2. HITL v2 cards:
//     - PlanReview → ExitPlanMode approval card
//     - Question   → structured AskUserQuestion card
//     - Permission → tool-call gate (bash / write / …)
//  3. Recovery protocol (docs/workstream-hitl-v2.md §4.3.1):
//     - GET  /session/events?thread_id=T&after=N → replay history
//     - GET  /decision/pending?thread_id=T       → unresolved decisions
//     - POST /decision/resolve                   → host-side resolve
//
// Layout:
//
//	examples/streaming-chat-copilotkit/
//	├── main.go                 # bootstrap
//	├── server.go               # HTTP host wiring
//	├── agui_run_session.go     # AG-UI stream forwarder + tee
//	├── thread_store.go         # in-memory recovery store
//	├── hitlmock/               # interactive HITL mock adapter
//	└── web/                    # Next.js + CopilotKit frontend, port 3000
//
// Run:
//
//	./examples/streaming-chat-copilotkit/start-all.sh mock       # recommended
//	./examples/streaming-chat-copilotkit/start-all.sh codex      # real codex backend
//	./examples/streaming-chat-copilotkit/start-all.sh claude     # real claude backend
//
// The `mock` backend triggers real sink.RequestDecision calls so you can
// click through PlanReview / Question / Permission cards end to end without
// relying on Phase 3 vendor HITL support.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func main() {
	addr := envOr("ADDR", ":8080")
	cors := envOr("CORS_ORIGIN", "*")
	cwd, _ := os.Getwd()

	sdk, driver := exampleutil.NewAGUIStreamingSDK(cwd)
	server := newAppServer(sdk, driver, cors)

	slog.Info("agent-adaptor AG-UI backend listening",
		"agent", driver,
		"addr", addr,
		"endpoint", "http://localhost"+addr+"/agent",
		"ui", "http://localhost:3000")
	if err := http.ListenAndServe(addr, server.routes()); err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
}
