// web-agui is the minimal-middleware AG-UI demo:
//
//	Browser (React + @ag-ui/client HttpAgent)
//	    ↓ POST /agent
//	Go backend (pkg/bridges/sse, Protocol=AGUI)
//	    ↓ local codex / claude / cursor CLI (env AGUI_AGENT)
//	subprocess (content stream when the selected adapter declares it)
//
// Compared with examples/showcases/web-copilotkit-hitl this example omits the
// Next.js / CopilotKit Runtime layer — the browser talks AG-UI directly
// to the Go backend via @ag-ui/client's HttpAgent. Fewer moving parts,
// zero CopilotKit dependencies, and the event stream is validated by the
// official AG-UI client code path without any runtime proxy in between.
//
// Run:
//
//	# Terminal 1
//	go run ./examples/showcases/web-agui
//
//	# Terminal 2
//	cd examples/showcases/web-agui/web
//	npm ci
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
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/sse"
)

func main() {
	if err := run(); err != nil {
		slog.Error("web-agui", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := ":8090"
	if a := os.Getenv("ADDR"); a != "" {
		addr = a
	}
	environment, err := exampleutil.NewTemporaryAgentEnvironment("web-agui")
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

	mux := http.NewServeMux()
	mux.Handle("/agent", exampleutil.WithRequestTimeout(sse.Handler(sdk, sse.Options{
		Protocol:          sse.AGUI,
		CORSAllowedOrigin: envOr("CORS_ORIGIN", "http://localhost:5173"),
		RunOptions: []agentadaptor.RunOption{
			exampleutil.AGUIExampleRunPolicy(),
		},
	}), 5*time.Minute))

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	httpServer := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	slog.Info("agent-adaptor AG-UI direct backend listening",
		"agent", driver,
		"addr", addr,
		"endpoint", exampleutil.HTTPURL(addr, "/agent"),
		"ui", "http://localhost:5173",
		"workspace", environment.WorkspaceDir,
		"profile", environment.ProfileDir)
	return exampleutil.ServeUntilSignal(httpServer)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
