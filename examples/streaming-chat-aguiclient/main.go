// streaming-chat-aguiclient is the minimal-middleware AG-UI demo:
//
//	Browser (React + @ag-ui/client HttpAgent)
//	    ↓ POST /agent
//	Go backend (pkg/bridges/sse, Protocol=AGUI)
//	    ↓ Codex app-server or Claude Code CLI (env AGUI_AGENT)
//	subprocess (token-level stream)
//
// Compared with examples/streaming-chat-copilotkit this example omits the
// Next.js / CopilotKit Runtime layer — the browser talks AG-UI directly
// to the Go backend via @ag-ui/client's HttpAgent. Fewer moving parts,
// zero CopilotKit dependencies, and the event stream is validated by the
// official AG-UI client code path without any runtime proxy in between.
//
// Run:
//
//	# Terminal 1
//	go run ./examples/streaming-chat-aguiclient
//
//	# Terminal 2
//	cd examples/streaming-chat-aguiclient/web
//	npm install
//	npm run dev
//	# open http://localhost:5173
//
// The backend's wire contract is identical to the CopilotKit example:
// both mount sse.Handler with Protocol=AGUI, so any AG-UI-compliant
// client works without changes.
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
	addr := ":8090"
	if a := os.Getenv("ADDR"); a != "" {
		addr = a
	}
	cwd, _ := os.Getwd()

	sdk, driver := exampleutil.NewAGUIStreamingSDK(cwd)

	mux := http.NewServeMux()
	mux.Handle("/agent", sse.Handler(sdk, sse.Options{
		Protocol:          sse.AGUI,
		CORSAllowedOrigin: envOr("CORS_ORIGIN", "http://localhost:5173"),
		RunOptions: []agentadaptor.RunOption{
			exampleutil.AGUIExampleRunPolicy(),
		},
	}))

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	slog.Info("agent-adaptor AG-UI direct backend listening",
		"agent", driver,
		"addr", addr,
		"endpoint", "http://localhost"+addr+"/agent",
		"ui", "http://localhost:5173")
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
