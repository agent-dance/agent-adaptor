// web-chat/aguiclient is the minimal-middleware AG-UI demo:
//
//	Browser (React + @ag-ui/client HttpAgent)
//	    ↓ POST /agent
//	Go backend (bridges/sse, Protocol=AGUI)
//	    ↓ local codex / claude / cursor CLI (env AGUI_AGENT)
//	subprocess (typed Event stream; token-level only when the Driver supports it)
//
// Compared with examples/web-chat/copilotkit this example omits the
// Next.js / CopilotKit Runtime layer — the browser talks AG-UI directly
// to the Go backend via @ag-ui/client's HttpAgent. Fewer moving parts,
// zero CopilotKit dependencies, and the event stream is validated by the
// official AG-UI client code path without any runtime proxy in between.
//
// The backend is one Agent value plus one bridge call. The bridge accepts
// adaptor.Runner, and because the Agent has a thread store it binds each
// request to a collision-free, namespace-encoded Thread key derived from the
// AG-UI threadId.
//
// Run:
//
//	# Terminal 1
//	go run ./examples/web-chat/aguiclient
//
//	# Terminal 2
//	cd examples/web-chat/aguiclient/web
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

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const (
	defaultListenAddr = "127.0.0.1:8090"
	// The Vite UI is the only cross-origin client enabled by default.
	defaultCORSOrigin = "http://localhost:5173"
)

func main() {
	addr := envOr("ADDR", defaultListenAddr)
	cwd, _ := os.Getwd()

	ai, agentName := exampleutil.NewAGUIStreamingAgent(cwd)

	mux := http.NewServeMux()
	mux.Handle("/agent", sse.Handler(ai, sse.Options{
		Protocol:          sse.AGUI,
		CORSAllowedOrigin: envOr("CORS_ORIGIN", defaultCORSOrigin),
		// Call-scope overrides appended to every Stream the handler starts.
		Options: []adaptor.CallOption{exampleutil.AGUIExamplePolicy()},
	}))

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	slog.Info("agent-adaptor AG-UI direct backend listening",
		"agent", agentName,
		"addr", addr,
		"endpoint", "http://"+addr+"/agent",
		"ui", defaultCORSOrigin)
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
