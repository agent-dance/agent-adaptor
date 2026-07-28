// web-chat/copilotkit combines the SDK's unified Event stream with
// CopilotKit's React UI through the AG-UI protocol.
//
// What this example shows:
//
//  1. Standard AG-UI streaming (text / tool_call / thinking)
//  2. Approval cards — the request arrives as a
//     *adaptor.ApprovalRequest event on the same stream and carries its own
//     responder:
//     - PlanReview → ExitPlanMode approval card
//     - Question   → structured AskUserQuestion card
//     - Permission → tool-call gate (bash / write / …)
//  3. Host recovery protocol:
//     - GET  /session/events?thread_id=T&after=N → replay history
//     - GET  /decision/pending?thread_id=T       → unresolved approvals
//     - POST /decision/resolve                   → host-side resolve
//
// One *adaptor.Agent serves one Thread per browser threadId and one Stream per
// request. A single range over stream.Events(), followed by stream.Result(),
// handles operational events, approvals, and the terminal result.
//
// Layout:
//
//	examples/web-chat/copilotkit/
//	├── main.go                 # bootstrap
//	├── server.go               # HTTP host wiring
//	├── agui_run_session.go     # AG-UI stream forwarder + tee
//	├── thread_store.go         # memory / JSONL recovery store
//	└── web/                    # Next.js + CopilotKit frontend, port 3000
//
// Run:
//
//	./examples/web-chat/copilotkit/start-all.sh codex
//	./examples/web-chat/copilotkit/start-all.sh claude
//	./examples/web-chat/copilotkit/start-all.sh cursor
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const (
	defaultListenAddr = "127.0.0.1:8080"
	// The browser recovery/HITL calls come from the local Next.js UI. The
	// server-side CopilotRuntime request does not require CORS.
	defaultCORSOrigin = "http://localhost:3000"
)

func main() {
	addr := envOr("ADDR", defaultListenAddr)
	cors := envOr("CORS_ORIGIN", defaultCORSOrigin)
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
		"endpoint", "http://"+addr+"/agent",
		"ui", "http://localhost:3000")
	if err := http.ListenAndServe(addr, server.routes()); err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
}
