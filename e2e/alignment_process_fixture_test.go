package e2e_test

// This fixture is independent of the owner fixtures. It only emits hand-written
// provider protocol and stores actual session bytes; all oracles use public API.
import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codebuddy"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
)

const alignmentFixtureEnv = "AA_T20_FIXTURE"
const alignmentSchema = `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`

type alignmentLog struct {
	Kind      string `json:"kind"`
	PID       int    `json:"pid"`
	Prompt    string `json:"prompt,omitempty"`
	Resume    string `json:"resume,omitempty"`
	Profile   string `json:"profile,omitempty"`
	Native    bool   `json:"native,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	TokenHash string `json:"token_hash,omitempty"`
	Carrier   string `json:"carrier,omitempty"`
	Overlap   []int  `json:"overlap,omitempty"`
	Previous  string `json:"previous,omitempty"`
}

func init() {
	if p := os.Getenv(alignmentFixtureEnv); p != "" {
		if p == "contender" {
			os.Exit(alignmentContender())
		}
		os.Exit(alignmentProvider(p))
	}
}
func alignmentAppend(entry alignmentLog) {
	entry.PID = os.Getpid()
	f, e := os.OpenFile(os.Getenv("AA_T20_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		panic(e)
	}
	defer f.Close()
	if e = json.NewEncoder(f).Encode(entry); e != nil {
		panic(e)
	}
}
func alignmentArg(key string) string {
	for i, a := range os.Args[1:] {
		if a == key && i+2 < len(os.Args) {
			return os.Args[i+2]
		}
		if strings.HasPrefix(a, key+"=") {
			return strings.TrimPrefix(a, key+"=")
		}
	}
	return ""
}
func alignmentHas(key string) bool {
	for _, a := range os.Args[1:] {
		if a == key || strings.HasPrefix(a, key+"=") {
			return true
		}
	}
	return false
}
func alignmentEmit(v any) {
	if e := json.NewEncoder(os.Stdout).Encode(v); e != nil {
		panic(e)
	}
}
func alignmentDeadlineDelay(prompt string) {
	if strings.Contains(prompt, "partial-deadline") {
		// Exercise delayed first output without depending on host scheduling.
		// This exceeds the old 250ms deadline only in the deadline audit case.
		time.Sleep(750 * time.Millisecond)
	}
}
func alignmentObject(v any) map[string]any { m, _ := v.(map[string]any); return m }
func alignmentText(v any) string           { s, _ := v.(string); return s }

func alignmentProvider(provider string) int {
	prof := os.Getenv(map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codebuddy": "CODEBUDDY_CONFIG_DIR", "cursor": "CURSOR_HOME", "codex": "CODEX_HOME"}[provider])
	native := alignmentHas("--json-schema")
	start := alignmentLog{Kind: "start", Profile: prof, Native: native, Resume: alignmentArg("--resume")}
	if raw, err := os.ReadFile(os.Getenv("AA_T20_LOG")); err == nil {
		seen := map[int]bool{}
		for _, line := range strings.Split(string(raw), "\n") {
			var l alignmentLog
			if json.Unmarshal([]byte(line), &l) == nil && l.Kind == "start" && !seen[l.PID] {
				seen[l.PID] = true
				if testutil.ProcessAlive(l.PID) {
					start.Overlap = append(start.Overlap, l.PID)
				}
			}
		}
	}
	alignmentAppend(start)
	defer alignmentAppend(alignmentLog{Kind: "exit"})
	if os.Getenv("AA_T20_FAIL_BOOT") == "1" {
		if e := os.Mkdir(os.Getenv("AA_T20_LOG")+".failed", 0700); e == nil {
			if provider == "claude" {
				// Claude has no initialization acknowledgement. Read an actual byte
				// before failing so this is unambiguously past prompt delivery.
				var first [1]byte
				if _, e := io.ReadFull(os.Stdin, first[:]); e != nil {
					return 72
				}
				alignmentAppend(alignmentLog{Kind: "partial-input"})
			}
			return 71
		}
	}
	if provider == "codex" {
		return alignmentCodex(prof)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	resume := alignmentArg("--resume")
	session := "session-t20"
	if resume != "" {
		session = resume
	}
	emitTurn := func(prompt string) int {
		if strings.Contains(prompt, "recover-history") && resume == "" {
			session = "recovered-t20"
		}
		entry := alignmentLog{Kind: "prompt", Prompt: prompt, Profile: prof, Native: native, Resume: resume}
		if provider == "cursor" {
			if e := alignmentDiskSession(prof, prompt, resume, &entry); e != nil {
				fmt.Fprintln(os.Stderr, "session session-t20 not found")
				alignmentAppend(entry)
				return 1
			}
		}
		alignmentAppend(entry)
		alignmentDeadlineDelay(prompt)
		alignmentEmit(map[string]any{"type": "system", "subtype": "init", "session_id": session, "model": "fixture-model"})
		fmt.Fprintln(os.Stderr, "t20-stderr")
		if strings.Contains(prompt, "ask-") {
			name := "AskUserQuestion"
			input := map[string]any{"questions": []any{map[string]any{"question": "Destination?", "header": "Target", "multiSelect": false, "options": []any{map[string]any{"label": "docs", "description": "docs"}, map[string]any{"label": "src", "description": "src"}}}}}
			if strings.Contains(prompt, "ask-plan") {
				name = "ExitPlanMode"
				input = map[string]any{"plan": "check docs"}
			}
			if strings.Contains(prompt, "ask-permission") {
				name = "Read"
				input = map[string]any{"file_path": "README.md"}
			}
			// A tool-use stop is intermediate and must keep stdin available.
			alignmentEmit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}}})
			alignmentEmit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_stop"}})
			alignmentEmit(map[string]any{"type": "control_request", "request_id": "t20-approval", "request": map[string]any{"subtype": "can_use_tool", "tool_name": name, "tool_use_id": "t20-tool", "input": input}})
			if !scanner.Scan() {
				return 72
			}
			var response map[string]any
			if json.Unmarshal(scanner.Bytes(), &response) != nil || response["type"] != "control_response" {
				return 73
			}
			// Hand-written expected wire is deliberately separate from request construction.
			// Exact map equality also rejects invented question_type or other extra fields.
			expectedInput := `{"file_path":"README.md"}`
			if name == "AskUserQuestion" {
				expectedInput = `{"questions":[{"question":"Destination?","header":"Target","multiSelect":false,"options":[{"label":"docs","description":"docs"},{"label":"src","description":"src"}]}],"answers":{"Destination?":"docs"}}`
			} else if name == "ExitPlanMode" {
				expectedInput = `{"plan":"check docs"}`
			}
			var expected map[string]any
			if json.Unmarshal([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"t20-approval","response":{"behavior":"allow","toolUseID":"t20-tool","updatedInput":`+expectedInput+`}}}`), &expected) != nil {
				return 79
			}
			if !reflect.DeepEqual(response, expected) {
				fmt.Fprintln(os.Stderr, "t20-invalid-approval-wire")
				return 79
			}
			body := alignmentObject(alignmentObject(response["response"])["response"])
			alignmentAppend(alignmentLog{Kind: "answer", Prompt: alignmentText(body["behavior"])})
			if body["behavior"] != "allow" {
				return 74
			}

		}
		if strings.Contains(prompt, "partial") || (strings.Contains(prompt, "nonzero") && !strings.Contains(prompt, "terminal-nonzero")) || strings.Contains(prompt, "malformed") || strings.Contains(prompt, "missing") {
			alignmentEmit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start", "message": map[string]any{"id": "partial-t20", "usage": map[string]any{"input_tokens": 7, "output_tokens": 3}}}})
			alignmentEmit(map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{"model": "fixture-model", "content": []any{map[string]any{"type": "text", "text": "partial-text"}}, "usage": map[string]any{"input_tokens": 7, "output_tokens": 3}}})
			if strings.Contains(prompt, "partial") {
				alignmentAppend(alignmentLog{Kind: "barrier"})
				time.Sleep(20 * time.Second)
				return 75
			}
			if strings.Contains(prompt, "nonzero") {
				return 29
			}
			if strings.Contains(prompt, "malformed") {
				fmt.Fprintln(os.Stdout, `{"type":`)
				return -1
			}
			return -1
		}
		if strings.Contains(prompt, "message-stop") {
			alignmentEmit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}}})
			alignmentEmit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_stop"}})
		}
		result := map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": session, "result": "answer-text", "usage": map[string]any{"input_tokens": 7, "output_tokens": 3}}
		if native {
			result["structured_output"] = map[string]any{"value": "ok"}
		}
		if strings.Contains(prompt, "ask-permission") && !native {
			result["result"] = `{"value":"ok"}`
		}
		if strings.Contains(prompt, "invalid-schema") {
			result["structured_output"] = map[string]any{"value": 123}
		}
		if strings.Contains(prompt, "provider-failure") {
			result["is_error"] = true
			result["subtype"] = "error_during_execution"
			result["errors"] = []string{"fixture-failure"}
		}
		alignmentEmit(result)
		if strings.Contains(prompt, "terminal-race") {
			alignmentAppend(alignmentLog{Kind: "terminal-sent"})
		}
		if strings.Contains(prompt, "terminal-nonzero") {
			return 29
		}
		if provider == "cursor" {
			return 0
		}
		resume = session
		return 0
	}
	if provider == "cursor" {
		b, _ := io.ReadAll(os.Stdin)
		code := emitTurn(string(b))
		if code < 0 {
			return 0
		}
		return code
	}
	if alignmentArg("--input-format") != "stream-json" {
		b, _ := io.ReadAll(os.Stdin)
		prompt := string(b)
		if provider == "codebuddy" {
			prompt = os.Args[len(os.Args)-1]
		}
		code := emitTurn(prompt)
		if code < 0 {
			return 0
		}
		return code
	}
	for scanner.Scan() {
		var msg map[string]any
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			return 76
		}
		switch msg["type"] {
		case "control_request":
			alignmentEmit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": msg["request_id"], "response": map[string]any{}}})
		case "user":
			content := alignmentObject(msg["message"])["content"]
			prompt := alignmentText(content)
			if arr, ok := content.([]any); ok {
				for _, v := range arr {
					if s := alignmentText(alignmentObject(v)["text"]); s != "" {
						prompt += s
					}
				}
			}
			if code := emitTurn(prompt); code != 0 {
				if code < 0 {
					return 0
				}
				return code
			}
		}
	}
	alignmentAppend(alignmentLog{Kind: "eof"})
	// A suffix emitted only after EOF makes input-close and complete Raw testable.
	fmt.Fprintln(os.Stderr, "t20-eof-tail")
	fmt.Fprint(os.Stdout, "\n \t\n")
	return 0
}

func alignmentDiskSession(prof, prompt, resume string, entry *alignmentLog) error {
	raw, e := os.ReadFile(filepath.Join(prof, "mcp.json"))
	if e != nil {
		return e
	}
	var cfg struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if e = json.Unmarshal(raw, &cfg); e != nil {
		return e
	}
	host := cfg.Servers["agent-adaptor-tools"]
	entry.Endpoint = host.URL
	for _, h := range host.Headers {
		if strings.HasPrefix(h, "Bearer ${env:") {
			name := strings.TrimSuffix(strings.TrimPrefix(h, "Bearer ${env:"), "}")
			entry.Carrier = name
			token := os.Getenv(name)
			hash := sha256.Sum256([]byte(token))
			entry.TokenHash = hex.EncodeToString(hash[:])
			// Credentials use a private control file; the event ledger only holds a digest.
			secretDir := os.Getenv("AA_T20_LOG") + ".credentials"
			if e := os.MkdirAll(secretDir, 0700); e != nil {
				return e
			}
			if e := os.WriteFile(filepath.Join(secretDir, entry.TokenHash), []byte(token), 0600); e != nil {
				return e
			}
		}
	}
	path := filepath.Join(prof, "projects", "session-t20.jsonl")
	if resume != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if !strings.HasPrefix(string(b), os.Getenv("AA_T20_NONCE")+"\n") {
			return fmt.Errorf("missing nonce")
		}
		entry.Previous = string(b)
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	if resume == "" {
		if e = os.WriteFile(path, []byte(os.Getenv("AA_T20_NONCE")+"\n"), 0600); e != nil {
			return e
		}
	}
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = fmt.Fprintln(f, prompt)
	return e
}

// Minimal official app-server JSON-RPC peer; every turn has its own ID.
func alignmentCodex(prof string) int {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	n := 0
	for scanner.Scan() {
		var q map[string]any
		if json.Unmarshal(scanner.Bytes(), &q) != nil {
			return 78
		}
		method := alignmentText(q["method"])
		params := alignmentObject(q["params"])
		reply := func(v any) { alignmentEmit(map[string]any{"jsonrpc": "2.0", "id": q["id"], "result": v}) }
		notify := func(m string, p any) { alignmentEmit(map[string]any{"jsonrpc": "2.0", "method": m, "params": p}) }
		switch method {
		case "initialize":
			reply(map[string]any{"userAgent": "t20-fixture"})
		case "thread/start", "thread/resume", "thread/fork":
			reply(map[string]any{"thread": map[string]any{"id": "session-t20"}, "model": "fixture-model"})
		case "turn/start":
			n++
			turn := fmt.Sprintf("turn-%d", n)
			prompt := ""
			for _, v := range params["input"].([]any) {
				prompt += alignmentText(alignmentObject(v)["text"])
			}
			alignmentAppend(alignmentLog{Kind: "prompt", Prompt: prompt, Profile: prof})
			alignmentDeadlineDelay(prompt)
			reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress", "items": []any{}}})
			notify("turn/started", map[string]any{"threadId": "session-t20", "turn": map[string]any{"id": turn, "status": "inProgress", "items": []any{}}})
			text := "answer-text"
			if strings.Contains(prompt, "partial") {
				text = "partial-text"
			}
			item := map[string]any{"type": "agentMessage", "id": "message-t20", "text": text}
			notify("item/started", map[string]any{"threadId": "session-t20", "turnId": turn, "item": item})
			notify("item/agentMessage/delta", map[string]any{"threadId": "session-t20", "turnId": turn, "itemId": "message-t20", "delta": text})
			notify("item/completed", map[string]any{"threadId": "session-t20", "turnId": turn, "item": item})
			fmt.Fprintln(os.Stderr, "t20-stderr")
			notify("thread/tokenUsage/updated", map[string]any{"threadId": "session-t20", "turnId": turn, "tokenUsage": map[string]any{"last": map[string]any{"inputTokens": 7, "outputTokens": 3, "totalTokens": 10, "cachedInputTokens": 0, "reasoningOutputTokens": 0}, "total": map[string]any{"inputTokens": 7, "outputTokens": 3, "totalTokens": 10, "cachedInputTokens": 0, "reasoningOutputTokens": 0}}})
			if strings.Contains(prompt, "partial") {
				alignmentAppend(alignmentLog{Kind: "barrier"})
				time.Sleep(20 * time.Second)
				return 75
			}
			notify("turn/completed", map[string]any{"threadId": "session-t20", "turn": map[string]any{"id": turn, "status": "completed", "items": []any{item}}})
		case "turn/interrupt":
			reply(map[string]any{})
		}
	}
	return 0
}

type alignmentFixture struct {
	root, profile, log, provider string
	common                       driver.CommonConfig
}

func newAlignmentFixture(t *testing.T, provider string) *alignmentFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	prof := filepath.Join(root, "source")
	for _, p := range []string{home, prof} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	f := &alignmentFixture{root: root, profile: prof, log: filepath.Join(root, "ledger.jsonl"), provider: provider}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	env := []driver.EnvBinding{{Name: "AA_T20_NONCE", Value: hex.EncodeToString(nonce)}, {Name: "GORACE", Value: "atexit_sleep_ms=0 halt_on_error=1"}, {Name: alignmentFixtureEnv, Value: provider}, {Name: "AA_T20_LOG", Value: f.log}, {Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "XDG_CONFIG_HOME", Value: home}, {Name: "AGENT_ADAPTOR_LIVE_CONFORMANCE", Value: "0"}, {Name: "AGENT_ADAPTOR_E2E", Value: "0"}, {Name: "AGENT_ADAPTOR_UPDATE_API_GOLDEN", Value: "0"}}
	for _, name := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "CODEBUDDY_CONFIG_DIR", "CURSOR_HOME"} {
		env = append(env, driver.EnvBinding{Name: name, Value: ""})
	}
	f.common = driver.CommonConfig{Command: exe, CWD: root, Env: env, GracePeriod: 200 * time.Millisecond}
	return f
}
func (f *alignmentFixture) configured() driver.Driver {
	switch f.provider {
	case "claude":
		return claude.Driver(claude.Config{CommonConfig: f.common})
	case "codebuddy":
		return codebuddy.Driver(codebuddy.Config{CommonConfig: f.common})
	case "codex":
		return codex.Driver(codex.Config{CommonConfig: f.common})
	default:
		return cursor.Driver(cursor.Config{CommonConfig: f.common})
	}
}
func (f *alignmentFixture) agent(t *testing.T, opts ...adaptor.Option) *adaptor.Agent {
	t.Helper()
	all := []adaptor.Option{adaptor.WithProfile(profile.Dedicated(f.profile)), adaptor.WithThreadStore(memory.NewStore())}
	all = append(all, opts...)
	a := adaptor.New(f.configured(), all...)
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), 4*time.Second)
		defer c()
		if e := alignmentClose(t, a, ctx); e != nil {
			t.Errorf("cleanup Close: %v", e)
		}
	})
	return a
}
func (f *alignmentFixture) logs(t *testing.T) []alignmentLog {
	t.Helper()
	raw, e := os.ReadFile(f.log)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		t.Fatal(e)
	}
	var out []alignmentLog
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l == "" {
			continue
		}
		var e alignmentLog
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}
func (f *alignmentFixture) wait(t *testing.T, kind string, n int) []alignmentLog {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		var found []alignmentLog
		for _, l := range f.logs(t) {
			if l.Kind == kind {
				found = append(found, l)
			}
		}
		if len(found) >= n {
			return found
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture %s count=%d want >=%d", kind, len(found), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func alignmentContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 8*time.Second)
}
