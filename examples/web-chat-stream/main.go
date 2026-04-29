// web-chat-stream is the agent-adaptor spotlight for "ship a streaming chat
// surface". It merges the legacy streaming-chat (CLI typing effect) and
// streaming-sse-server (HTTP SSE bridge) examples into a single binary with
// two modes: -mode=cli runs two prompts back-to-back to demonstrate token
// streaming + session continuation; -mode=server mounts pkg/bridges/sse so a
// browser can drive the same demo end-to-end.
//
// Story (CLI mode): one sessionKey, two rounds — round 1 opens a session
// with live token deltas; round 2 reuses the same session and the agent picks
// up where it left off. Stderr prints `[session reused: <id> · turns 2 ·
// age <Δ>]` so the host can verify continuation in one glance.
//
// Story (server mode): a host gateway lights up by mounting
// `sse.Handler(sdk, sse.Options{Protocol: sse.AGUI})` at /v1/chat plus an
// inline index.html. Two prompts in the browser share `sessionKey:
// "demo/web"` and the second message visibly continues from the first.
//
// Artifacts (CLI mode):
//   - typing-effect transcript on stdout (text deltas), reasoning/tool
//     decoration on stderr
//   - .spotlight/web-chat-stream/sse-capture.ndjson (round 1 frames, one
//     StreamPayload per line)
//   - .spotlight/web-chat-stream/last-run.md (5 sections mirroring
//     walkthrough.md)
//
// Artifacts (server mode):
//   - HTTP listener at -addr serving /v1/chat (AG-UI SSE) and / (HTML)
//   - no last-run.md (a long-running service has no clean completion point)
//
// The example never panics on adapters that lack streaming or
// authentication: it captures real failure modes and surfaces them through
// stderr decoration + the transcript section.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/sse"
)

const (
	storyText    = "Two prompts share one sessionKey: tokens stream live and round 2 picks up where round 1 left off."
	storyTo      = "Web IDE · Cursor-like 聊天面板 · CopilotKit · 客服坐席助手 · 内部 review 助手"
	spotlightDir = ".spotlight/web-chat-stream"

	sessionNamespace = "examples"
	sessionKey       = "web-chat-stream"

	round1Prompt = "Write three short lines about agents."
	round2Prompt = "Now add a fourth line that summarizes the three you just wrote."

	browserSessionKey = "demo/web"
)

// indexHTML is the tiny single-page demo served at GET /. It deliberately
// hard-codes sessionKey="demo/web" so two prompts in sequence (or after a
// page reload) reuse the same SDK session and visibly continue. The host
// can drop this <script> block into any React/Vue project unchanged.
const indexHTML = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>agent-adaptor · web-chat-stream</title>
  <style>
    body { font-family: system-ui, sans-serif; margin: 2em; max-width: 760px; }
    #out { white-space: pre-wrap; border: 1px solid #ccc; padding: 1em; min-height: 14em; border-radius: 6px; }
    #in  { width: 100%; box-sizing: border-box; padding: 0.5em; font-family: inherit; }
    button { margin-top: 0.5em; padding: 0.45em 1em; }
    .meta { color: #666; font-size: 0.85em; }
    .turn { color: #5b6470; font-size: 0.85em; margin-top: 0.6em; }
  </style>
</head>
<body>
  <h1>agent-adaptor · web-chat-stream</h1>
  <p class="meta">
    POST /v1/chat ⇢ AG-UI events over SSE ·
    sessionKey = <code>demo/web</code> · two prompts in a row continue the same session.
  </p>
  <div id="out"></div>
  <p class="turn">turn <span id="turn">0</span> · sessionKey: demo/web</p>
  <textarea id="in" rows="3" placeholder="Try: Write three short lines about agents."></textarea>
  <button id="send">Send</button>
  <script>
    const out = document.getElementById('out');
    const input = document.getElementById('in');
    const turnEl = document.getElementById('turn');
    let turn = 0;
    document.getElementById('send').addEventListener('click', async () => {
      const prompt = input.value.trim();
      if (!prompt) return;
      turn += 1;
      turnEl.textContent = String(turn);
      out.textContent += "\n\n[turn " + turn + "] > " + prompt + "\n";
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
	agentFlag := flag.String("agent", "", "Local CLI agent: "+exampleutil.SupportedAgents())
	modelFlag := flag.String("model", "", "Model override")
	commandFlag := flag.String("command", "", "Optional explicit local CLI command")
	mode := flag.String("mode", "cli", "Execution mode: cli or server")
	prompt := flag.String("prompt", round1Prompt, "Round 1 prompt (round 2 reuses sessionKey to demonstrate continuation)")
	cancelAfter := flag.Duration("cancel-after", 0, "CLI only: cancel round 1 after this duration to demonstrate cancellation (0 disables)")
	addr := flag.String("addr", ":8080", "Server mode listen address")
	timeout := flag.Duration("timeout", 5*time.Minute, "CLI mode overall timeout")
	captureSSE := flag.String("capture-sse", filepath.Join(spotlightDir, "sse-capture.ndjson"), "CLI only: ndjson path for round-1 stream events (one StreamPayload per line)")
	flag.Parse()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve cwd")
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agentFlag, *modelFlag, *commandFlag, cwd)

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(agentCfg)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "cli":
		runCLI(sdk, agentCfg, *prompt, *cancelAfter, *timeout, *captureSSE)
	case "server":
		runServer(sdk, agentCfg, *addr)
	default:
		exampleutil.Fatalf("unknown -mode %q (expected cli or server)", *mode)
	}
}

// ──────────────────────────────────────────────────────────────────────
// CLI mode — two prompts back-to-back, real-time typing, capture round 1.
// ──────────────────────────────────────────────────────────────────────

// roundOutcome is the per-round summary used by the transcript / evidence /
// last-run.md sections. Failures are captured rather than panicked so the
// host sees real failure modes (unauthenticated CLI, no streaming, etc.).
type roundOutcome struct {
	Index           int
	RunID           string
	SessionID       string
	SessionReused   bool
	SessionCreated  bool
	StartedAt       time.Time
	WaitedAt        time.Time
	Frames          int
	TextDeltas      int
	TextChars       int
	ReasoningDeltas int
	ToolCalls       int
	Output          string
	StartErr        error
	WaitErr         error
	Failure         *agentadaptor.RunFailure
	StreamSupported bool
}

func runCLI(sdk agentadaptor.SDK, agentCfg exampleutil.LiveAgentConfig, prompt string, cancelAfter, timeout time.Duration, captureSSEPath string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cap, err := newCaptureWriter(captureSSEPath)
	exampleutil.Must(err, "create sse capture file %s", captureSSEPath)
	defer cap.Close()

	fmt.Fprintf(os.Stderr, "[mode=cli · agent=%s · model=%s · capture=%s]\n", agentCfg.Agent, agentCfg.Model, captureSSEPath)
	fmt.Fprintln(os.Stderr, "━━━ Round 1 (sessionKey: "+sessionNamespace+"/"+sessionKey+") ━━━━━━━━━━━━━━━━━")

	r1 := runStreamRound(ctx, sdk, 1, prompt, cancelAfter, cap)

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "━━━ Round 2 (same sessionKey) ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	r2 := runStreamRound(ctx, sdk, 2, round2Prompt, 0, nil)

	// Round 2 stderr verdict: this is the literal `[session reused: ...]`
	// line the spotlight contract wants the host to grep.
	switch {
	case r2.SessionReused && r2.SessionID != "" && r2.SessionID == r1.SessionID:
		age := r2.StartedAt.Sub(r1.StartedAt).Round(time.Millisecond)
		fmt.Fprintf(os.Stderr, "[session reused: %s · turns 2 · age %s]\n", r2.SessionID, age)
	case r2.WaitErr != nil || r2.StartErr != nil:
		err := r2.WaitErr
		if err == nil {
			err = r2.StartErr
		}
		fmt.Fprintf(os.Stderr, "[session continuation: round 2 errored: %v]\n", err)
	case r2.Failure != nil:
		fmt.Fprintf(os.Stderr, "[session continuation failed: %s · %s]\n", r2.Failure.Code, clip(r2.Failure.Message, 96))
	default:
		fmt.Fprintf(os.Stderr, "[session continuation: not reused (created=%v session=%s) — round 1 may not have produced a checkpoint]\n", r2.SessionCreated, displayID(r2.SessionID))
	}

	transcript := renderTranscript(r1, r2, agentCfg)
	fmt.Println()
	fmt.Println(transcript)

	evidence := renderSessionEvidence(r1, r2)
	fmt.Println(evidence)

	storyBanner := exampleutil.PrintStoryBanner(storyText, storyTo)
	artifactPaths := []string{
		captureSSEPath,
		filepath.Join(spotlightDir, "last-run.md"),
		"examples/web-chat-stream/main.go",
		"examples/web-chat-stream/walkthrough.md",
	}
	artifactsBanner := exampleutil.PrintArtifactsBanner(artifactPaths)
	tryNextBanner := exampleutil.PrintTryNextBanner("go run ./examples/web-chat-stream -mode=server -agent=" + agentCfg.Agent)

	exampleutil.MustWriteLastRunMarkdown(filepath.Join(spotlightDir, "last-run.md"), []exampleutil.LastRunSection{
		{Title: "Story", Body: storyBanner},
		{Title: "Two-round transcript", Body: exampleutil.FenceCodeBlock("", transcript)},
		{Title: "Session continuation evidence", Body: exampleutil.FenceCodeBlock("", evidence)},
		{Title: "Artifacts", Body: artifactsBanner},
		{Title: "Try next", Body: tryNextBanner},
	})
}

// runStreamRound consumes one round's StreamEvents, mirroring text deltas to
// stdout (the host-visible typing effect) and reasoning / tool / lifecycle
// markers to stderr (the host-visible scaffolding). When capture is non-nil
// every StreamPayload is appended as ndjson so the host can `cat | jq` the
// raw frames after the run.
//
// The function never panics. Both Start() and Wait() errors are recorded on
// roundOutcome so an unauthenticated or non-streaming CLI surfaces in the
// transcript / evidence sections rather than aborting the whole spotlight.
func runStreamRound(ctx context.Context, sdk agentadaptor.SDK, idx int, prompt string, cancelAfter time.Duration, capture *captureWriter) roundOutcome {
	out := roundOutcome{Index: idx, StartedAt: time.Now()}

	handle, err := sdk.Start(ctx, prompt,
		agentadaptor.WithStreaming(),
		agentadaptor.WithSessionKey(sessionNamespace, sessionKey),
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly),
	)
	if err != nil {
		out.StartErr = err
		fmt.Fprintf(os.Stderr, "[round %d] start error: %v\n", idx, err)
		return out
	}
	out.RunID = handle.RunID()
	fmt.Fprintf(os.Stderr, "[round %d run=%s]\n", idx, displayID(handle.RunID()))

	if cancelAfter > 0 {
		go func() {
			select {
			case <-time.After(cancelAfter):
				_ = handle.Cancel(ctx)
				fmt.Fprintf(os.Stderr, "[round %d] sent cancel after %s\n", idx, cancelAfter)
			case <-ctx.Done():
			}
		}()
	}

	go func() {
		for range handle.Events() {
		}
	}()

	for ev := range handle.StreamEvents() {
		out.Frames++
		capture.Append(ev)
		switch ev.Kind {
		case agentadaptor.StreamTextContent:
			fmt.Print(ev.Delta)
			out.TextDeltas++
			out.TextChars += len(ev.Delta)
			out.StreamSupported = true
		case agentadaptor.StreamReasoningContent:
			fmt.Fprint(os.Stderr, ev.Delta)
			out.ReasoningDeltas++
			out.StreamSupported = true
		case agentadaptor.StreamToolCallStart:
			fmt.Fprintf(os.Stderr, "\n[tool:%s]\n", ev.Name)
			out.ToolCalls++
			out.StreamSupported = true
		case agentadaptor.StreamRunFinished:
			fmt.Println()
			if ev.Usage != nil {
				fmt.Fprintf(os.Stderr, "[round %d usage input=%d output=%d cached=%d]\n", idx, ev.Usage.InputTokens, ev.Usage.OutputTokens, ev.Usage.CachedInputTokens)
			}
		case agentadaptor.StreamRunError:
			msg := "unknown"
			if ev.Error != nil {
				msg = ev.Error.Message
			}
			fmt.Fprintf(os.Stderr, "[round %d run error: %s]\n", idx, msg)
		}
	}

	result, err := handle.Wait(ctx)
	out.WaitedAt = time.Now()
	if err != nil && !errors.Is(err, context.Canceled) {
		out.WaitErr = err
		fmt.Fprintf(os.Stderr, "[round %d] wait error: %v\n", idx, err)
	}
	out.Output = strings.TrimSpace(result.Output)
	out.Failure = result.Failure
	if result.Session != nil {
		out.SessionID = result.Session.ID
		out.SessionReused = result.Session.Reused
		out.SessionCreated = result.Session.Created
	}
	// If the adapter did not stream but did produce a final output, mirror
	// it once so the host still sees content (cursor's batch path, for
	// instance).
	if !out.StreamSupported && len(result.Output) > 0 {
		fmt.Println(strings.TrimSpace(result.Output))
	}
	return out
}

// renderTranscript writes a per-round digest. It deliberately surfaces both
// `frames` and `text deltas` so the reader can immediately see whether the
// adapter actually streamed (frames > 1) or batched (frames = 0).
func renderTranscript(r1, r2 roundOutcome, agentCfg exampleutil.LiveAgentConfig) string {
	var b strings.Builder
	b.WriteString("Two-round transcript\n")
	for _, r := range []roundOutcome{r1, r2} {
		fmt.Fprintf(&b, "─ Round %d (run=%s · session=%s · reused=%v · created=%v)\n", r.Index, displayID(r.RunID), displayID(r.SessionID), r.SessionReused, r.SessionCreated)
		if r.StartErr != nil {
			fmt.Fprintf(&b, "  start_error  = %s\n", clip(r.StartErr.Error(), 96))
		}
		if r.WaitErr != nil {
			fmt.Fprintf(&b, "  wait_error   = %s\n", clip(r.WaitErr.Error(), 96))
		}
		fmt.Fprintf(&b, "  frames       = %d (text deltas %d, %d chars; reasoning %d; tools %d)\n", r.Frames, r.TextDeltas, r.TextChars, r.ReasoningDeltas, r.ToolCalls)
		if r.Output != "" {
			fmt.Fprintf(&b, "  output_head  = %s\n", clip(firstLine(r.Output), 96))
		}
		if r.Failure != nil {
			fmt.Fprintf(&b, "  failure      = %s · %s\n", string(r.Failure.Code), clip(r.Failure.Message, 96))
		}
		if !r.StreamSupported && r.Frames == 0 && r.WaitErr == nil && r.StartErr == nil && r.Failure == nil {
			fmt.Fprintf(&b, "  note         = adapter %s did not stream; output came from batch result\n", agentCfg.Agent)
		}
	}
	return b.String()
}

// renderSessionEvidence makes the continuation story explicit: which
// sessionKey was used, what the two rounds produced, and a one-line verdict.
func renderSessionEvidence(r1, r2 roundOutcome) string {
	var b strings.Builder
	b.WriteString("Session continuation evidence\n")
	fmt.Fprintf(&b, "  sessionKey       = %s/%s\n", sessionNamespace, sessionKey)
	fmt.Fprintf(&b, "  round 1 session  = %s (created=%v reused=%v)\n", displayID(r1.SessionID), r1.SessionCreated, r1.SessionReused)
	fmt.Fprintf(&b, "  round 2 session  = %s (created=%v reused=%v)\n", displayID(r2.SessionID), r2.SessionCreated, r2.SessionReused)
	switch {
	case r2.SessionReused && r2.SessionID == r1.SessionID && r1.SessionID != "":
		age := r2.StartedAt.Sub(r1.StartedAt).Round(time.Millisecond)
		fmt.Fprintf(&b, "  verdict          = continuation OK · same session · turns 2 · age %s\n", age)
	case r2.SessionReused && r2.SessionID != r1.SessionID:
		b.WriteString("  verdict          = WARNING reused=true but session id changed across rounds\n")
	default:
		b.WriteString("  verdict          = round 2 did NOT reuse — see transcript above for likely cause\n")
	}
	return b.String()
}

// captureWriter appends StreamPayloads as ndjson, one frame per line.
// json.Encoder appends a trailing newline automatically, which is exactly
// the ndjson contract. The mutex protects against concurrent writers — the
// streaming-chat path is single-goroutine but Cancel timers and operational
// drainers may race with it, and round-2 cleanup keeps the writer alive.
type captureWriter struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

func newCaptureWriter(path string) (*captureWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return &captureWriter{f: f, enc: enc}, nil
}

func (c *captureWriter) Append(p agentadaptor.StreamPayload) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.enc.Encode(p)
}

func (c *captureWriter) Close() error {
	if c == nil || c.f == nil {
		return nil
	}
	return c.f.Close()
}

// ──────────────────────────────────────────────────────────────────────
// Server mode — pkg/bridges/sse.Handler + inline index.html.
// ──────────────────────────────────────────────────────────────────────

// runServer mounts /v1/chat (sse.Handler with AG-UI protocol) and / (the
// inline index.html). It deliberately does NOT write a last-run.md: a
// long-running service has no clean "completion" point to mirror, and the
// dynamic walkthrough.md target is the browser experience itself.
func runServer(sdk agentadaptor.SDK, agentCfg exampleutil.LiveAgentConfig, addr string) {
	mux := http.NewServeMux()
	mux.Handle("/v1/chat", sse.Handler(sdk, sse.Options{
		Protocol:          sse.AGUI,
		CORSAllowedOrigin: "*",
		RunOptions: []agentadaptor.RunOption{
			exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly),
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

	browserURL := "http://localhost" + addr + "/"
	fmt.Fprintf(os.Stderr, "[mode=server · agent=%s · model=%s]\n", agentCfg.Agent, agentCfg.Model)
	fmt.Fprintf(os.Stderr, "Open: %s\n", browserURL)
	fmt.Fprintf(os.Stderr, "Try: curl -N -X POST http://localhost%s/v1/chat -H 'Content-Type: application/json' -d '{\"prompt\":\"Write three short lines about agents.\",\"sessionKey\":%q}'\n", addr, browserSessionKey)
	fmt.Fprintln(os.Stderr, "Press Ctrl+C to stop. The same sessionKey across requests demonstrates server-side session continuation.")

	if err := http.ListenAndServe(addr, mux); err != nil {
		exampleutil.Fatalf("listen %s: %v", addr, err)
	}
}

// ──────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────

func displayID(id string) string {
	if id == "" {
		return "(none)"
	}
	if len(id) <= 24 {
		return id
	}
	return id[:24] + "…"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func clip(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}
