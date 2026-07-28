// web-chat exposes an Agent over AG-UI Server-Sent Events with a
// single bridge call. Drivers without token deltas still surface their final
// Result.Text as one assistant message before the AG-UI terminal event.
//
// The bridge takes an adaptor.Runner, the interface both *Agent and *Thread
// satisfy. Hand it the Agent and it binds each request to
// agent.Thread(<key derived from the inbound threadId>); hand it a Thread and
// the conversation is pinned by the host instead.
//
// Usage:
//
//	go run ./examples/web-chat
//	# In another terminal:
//	curl -N -X POST http://localhost:8080/v1/chat \
//	    -H 'Content-Type: application/json' \
//	    -d '{"threadId":"demo","messages":[{"id":"1","role":"user","content":"write a haiku"}]}'
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

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/memory"
)

const (
	defaultListenAddr = "127.0.0.1:8080"
	// The bundled page is served by this process and uses a relative URL, so
	// its default request is same-origin and needs no CORS response headers.
	defaultCORSOrigin = ""
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
  <p class="meta">POST /v1/chat (AG-UI RunAgentInput) &rArr; AG-UI events over SSE</p>
  <div id="out"></div>
  <textarea id="in" rows="3" placeholder="Ask something..."></textarea>
  <button id="send">Send</button>
  <script>
    const out = document.getElementById('out');
    const input = document.getElementById('in');
    // threadId is the host-owned conversation key: keep it stable and the
    // server keeps talking to the same thread.
    const threadId = 'web-' + Math.random().toString(36).slice(2, 10);
    let turn = 0;
    document.getElementById('send').addEventListener('click', async () => {
      const prompt = input.value.trim();
      if (!prompt) return;
      out.textContent += "\n\n> " + prompt + "\n";
      input.value = "";
      const resp = await fetch('/v1/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          threadId,
          runId: threadId + '-' + (++turn),
          messages: [{ id: String(turn), role: 'user', content: prompt }],
        }),
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
	agentName := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	addrFlag := flag.String("addr", defaultListenAddr, "HTTP listen address (set ADDR or pass an explicit value to expose it beyond this machine)")
	corsFlag := flag.String("cors-origin", defaultCORSOrigin, "Optional Access-Control-Allow-Origin for a separately hosted client (or CORS_ORIGIN)")
	flag.Parse()

	addr := envOverride("ADDR", *addrFlag)
	corsOrigin := envOverride("CORS_ORIGIN", *corsFlag)
	cwd, err := os.Getwd()
	if err != nil {
		slog.Error("cwd", "err", err)
		os.Exit(1)
	}
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agentName, *model, *command, cwd)

	// The thread store is what turns an inbound threadId into a resumable
	// conversation; without it every request would start fresh.
	ai := adaptor.New(
		exampleutil.NewLiveDriver(agentCfg),
		adaptor.WithThreadStore(memory.NewStore()),
	)

	mux := http.NewServeMux()
	mux.Handle("/v1/chat", sse.Handler(ai, sse.Options{
		Protocol:          sse.AGUI,
		CORSAllowedOrigin: corsOrigin,
		// Options are call scope: appended to every Stream the handler starts.
		Options: []adaptor.CallOption{
			exampleutil.NonInteractive(agentCfg.Agent, adaptor.ReadOnly),
		},
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, indexHTML)
	})

	slog.Info("listening", "addr", addr, "docs", "http://"+addr+"/", "agent", agentCfg.Agent, "model", agentCfg.Model)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
}

func envOverride(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
