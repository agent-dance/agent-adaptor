// streaming-chat-copilotkit is a demo that combines the agent-adaptor SDK's
// streaming + HITL v2 backend with CopilotKit's React UI through the AG-UI
// protocol.
//
// What this example shows:
//
//  1. Standard AG-UI streaming (text / tool_call / thinking)
//  2. Approval cards, v1 form B — the request arrives as a
//     *adaptor.ApprovalRequest event on the same stream and carries its own
//     responder (decision D2):
//     - PlanReview → ExitPlanMode approval card
//     - Question   → structured AskUserQuestion card
//     - Permission → tool-call gate (bash / write / …)
//  3. Recovery protocol (docs/workstream-hitl-v2.md §4.3.1):
//     - GET  /session/events?thread_id=T&after=N → replay history
//     - GET  /decision/pending?thread_id=T       → unresolved approvals
//     - POST /decision/resolve                   → host-side resolve
//
// v1 shape: one *adaptor.Agent, one Thread per browser threadId, one
// Stream per request. The three goroutines the legacy backend needed
// (operational event drain, Wait, DecisionRequests watcher) collapse into a
// single range over stream.Events() plus one stream.Result() call.
//
// Layout:
//
//	examples/streaming-chat-copilotkit/
//	├── main.go                 # bootstrap
//	├── server.go               # HTTP host wiring
//	├── agui_run_session.go     # AG-UI stream forwarder + tee
//	├── thread_store.go         # memory / JSONL recovery store
//	└── web/                    # Next.js + CopilotKit frontend, port 3000
//
// Run:
//
//	./examples/streaming-chat-copilotkit/start-all.sh codex
//	./examples/streaming-chat-copilotkit/start-all.sh claude
//	./examples/streaming-chat-copilotkit/start-all.sh cursor
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

	ai, driver := exampleutil.NewAGUIStreamingAgent(cwd)
	server, err := newAppServer(ai, driver, cors)
	if err != nil {
		slog.Error("initialize server", "err", err)
		os.Exit(1)
	}

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
