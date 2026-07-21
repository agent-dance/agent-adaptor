// web-copilotkit-hitl combines the agent-adaptor SDK's
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
//	examples/showcases/web-copilotkit-hitl/
//	├── main.go                 # bootstrap
//	├── server.go               # HTTP host wiring
//	├── agui_run_session.go     # AG-UI stream forwarder + tee
//	├── thread_store.go         # in-memory recovery store
//	└── web/                    # Next.js + CopilotKit frontend, port 3000
//
// Run:
//
//	./examples/showcases/web-copilotkit-hitl/start-all.sh codex
//	./examples/showcases/web-copilotkit-hitl/start-all.sh claude
//	./examples/showcases/web-copilotkit-hitl/start-all.sh cursor
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func main() {
	if err := run(); err != nil {
		slog.Error("web-copilotkit-hitl", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := envOr("ADDR", ":8080")
	cors := envOr("CORS_ORIGIN", "*")
	environment, err := exampleutil.NewTemporaryAgentEnvironment("web-copilotkit-hitl")
	if err != nil {
		return err
	}
	defer environment.Cleanup()

	sdk, driver, err := exampleutil.BuildAGUIStreamingSDK(
		environment.WorkspaceDir,
		environment.CloneProfileOption(),
	)
	if err != nil {
		return err
	}
	app := newAppServer(sdk, driver, cors)
	httpServer := &http.Server{Addr: addr, Handler: app.routes(), ReadHeaderTimeout: 10 * time.Second}

	slog.Info("agent-adaptor AG-UI backend listening",
		"agent", driver,
		"addr", addr,
		"endpoint", exampleutil.HTTPURL(addr, "/agent"),
		"ui", "http://localhost:3000",
		"workspace", environment.WorkspaceDir,
		"profile", environment.ProfileDir)
	return exampleutil.ServeUntilSignal(httpServer)
}
