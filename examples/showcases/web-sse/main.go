// web-sse is a minimal example HTTP server that exposes the
// agent-adaptor streaming surface through AG-UI over Server-Sent Events.
//
// Usage:
//
//	go run ./examples/showcases/web-sse
//	# In another terminal:
//	curl -N -X POST http://localhost:8080/v1/chat \
//	    -H 'Content-Type: application/json' \
//	    -d '{"prompt":"write a haiku","sessionKey":"demo/user-1"}'
//
// Or open http://localhost:8080/ in a browser for a tiny JS chat page.
//
// The server requires the selected local CLI in PATH and existing
// authentication.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/sse"
)

const indexHTML = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>agent-adaptor streaming chat demo</title>
  <style>
    body { font-family: system-ui, sans-serif; margin: 2em; max-width: 720px; }
    #out { white-space: pre-wrap; border: 1px solid #ccc; padding: 1em; min-height: 12em; }
    #in { width: 100%; box-sizing: border-box; padding: 0.5em; }
    button { margin-top: 0.5em; }
    .meta { color: #666; font-size: 0.85em; }
  </style>
</head>
<body>
  <h1>agent-adaptor streaming chat</h1>
  <p class="meta">POST /v1/chat ⇢ AG-UI events over SSE</p>
  <div id="out"></div>
  <textarea id="in" rows="3" placeholder="Ask something..."></textarea>
  <button id="send">Send</button>
  <script>
    const out = document.getElementById('out');
    const input = document.getElementById('in');
    document.getElementById('send').addEventListener('click', async () => {
      const prompt = input.value.trim();
      if (!prompt) return;
      out.textContent += "\n\n> " + prompt + "\n";
      input.value = "";
      const resp = await fetch('/v1/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt, sessionKey: 'demo/web' }),
      });
      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buf = "";
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf("\n\n")) >= 0) {
          const frame = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          const line = frame.split("\n").find((l) => l.startsWith("data: "));
          if (!line) continue;
          try {
            const ev = JSON.parse(line.slice(6));
            if (ev.type === "TEXT_MESSAGE_CONTENT" && ev.delta) {
              out.textContent += ev.delta;
            } else if (ev.type === "RUN_FINISHED") {
              out.textContent += "\n[done]";
            } else if (ev.type === "RUN_ERROR") {
              out.textContent += "\n[error] " + (ev.message || "");
            }
          } catch (e) { /* ignore non-JSON frames */ }
        }
      }
    });
  </script>
</body>
</html>`

func main() {
	if err := run(); err != nil {
		slog.Error("web-sse", "err", err)
		os.Exit(1)
	}
}

func run() error {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	addrFlag := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()

	addr := *addrFlag
	if a := os.Getenv("ADDR"); a != "" {
		addr = a
	}
	environment, err := exampleutil.NewTemporaryAgentEnvironment("web-sse")
	if err != nil {
		return err
	}
	defer environment.Cleanup()
	agentCfg, err := exampleutil.TryResolveLiveAgentConfig(*agent, *model, *command, environment.WorkspaceDir)
	if err != nil {
		return err
	}
	agentCfg = environment.Configure(agentCfg)

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(agentCfg, environment.CloneProfileOption())),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	mux := http.NewServeMux()
	mux.Handle("/v1/chat", exampleutil.WithRequestTimeout(sse.Handler(sdk, sse.Options{
		Protocol:          sse.AGUI,
		CORSAllowedOrigin: "*",
		RunOptions: []agentadaptor.RunOption{
			exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly),
		},
	}), 5*time.Minute))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, indexHTML)
	})

	httpServer := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	slog.Info("listening", "addr", addr, "docs", exampleutil.HTTPURL(addr, "/"), "agent", agentCfg.Agent, "model", agentCfg.Model,
		"workspace", environment.WorkspaceDir, "profile", environment.ProfileDir)
	return exampleutil.ServeUntilSignal(httpServer)
}
