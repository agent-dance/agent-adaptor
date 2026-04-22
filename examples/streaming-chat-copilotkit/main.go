// streaming-chat-copilotkit is an end-to-end demo that combines the
// agent-adaptor SDK's streaming backend with CopilotKit's React UI
// through the AG-UI protocol.
//
// Layout:
//
//	examples/streaming-chat-copilotkit/
//	├── main.go                 # this Go backend, port 8080
//	└── web/                    # Next.js + CopilotKit frontend, port 3000
//
// Run:
//
//	# Terminal 1
//	go run ./examples/streaming-chat-copilotkit
//
//	# Terminal 2
//	cd examples/streaming-chat-copilotkit/web
//	npm install      # or pnpm install
//	npm run dev      # open http://localhost:3000
//
// Architecture:
//
//	Browser (React + <CopilotChat/>)
//	    ↓ POST /api/copilotkit
//	Next.js (CopilotRuntime with @ag-ui/client HttpAgent)
//	    ↓ POST http://localhost:8080/agent  (AG-UI RunAgentInput)
//	Go backend (pkg/bridges/sse, Protocol=AGUI)
//	    ↓ Codex app-server or Claude Code CLI (env AGUI_AGENT)
//	subprocess (token-level stream)
//
// The backend mounts a single /agent endpoint using the AG-UI protocol.
// sse.Handler owns the RunAgentInput decoding, prompt extraction,
// session binding, and AG-UI event emission — the host writes zero
// protocol glue.
package main

import (
	"log/slog"
	"net/http"
	"os"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/sse"
)

func main() {
	addr := ":8080"
	if a := os.Getenv("ADDR"); a != "" {
		addr = a
	}
	cwd, _ := os.Getwd()

	sdk, driver := exampleutil.NewAGUIStreamingSDK(cwd)

	mux := http.NewServeMux()
	mux.Handle("/agent", sse.Handler(sdk, sse.Options{
		Protocol:          sse.AGUI,
		CORSAllowedOrigin: envOr("CORS_ORIGIN", "http://localhost:3000"),
		RunOptions: []agentadaptor.RunOption{
			exampleutil.AGUIExampleRunPolicy(),
		},
	}))

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	slog.Info("agent-adaptor AG-UI backend listening",
		"agent", driver,
		"addr", addr,
		"endpoint", "http://localhost"+addr+"/agent",
		"ui", "http://localhost:3000")
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
