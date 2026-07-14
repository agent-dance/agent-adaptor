// Command probe 是抓帧探针：拉起真实 `codebuddy` CLI，跑一遍 ACP 或 headless
// 流程，把每一条收发的原始帧落盘为 JSONL，作为验证套件断言与假 CLI 回放的
// 事实来源（source of truth）。这是采集工具，不做断言。
//
// 用法：
//
//	# ACP：initialize→session/new→session/prompt，自动 approve 首个 allow 选项
//	go run ./tests/probe -mode acp -prompt "..." -out tests/probe/fixtures
//
//	# headless：codebuddy --print --output-format stream-json --verbose <prompt>
//	go run ./tests/probe -mode headless -prompt "..." -out tests/probe/fixtures
//
// 探针不依赖 agent-adaptor 的任何内部包，直接以 ndjson/stream-json 与 CLI 对话，
// 以保证抓到的是"CLI 真实吐出的字节"而非经 driver 归约后的结构。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func main() {
	var (
		mode       = flag.String("mode", "acp", "acp | headless")
		prompt     = flag.String("prompt", "Create a file named hello.txt in the current directory containing the text hi. Use your file-editing tool. Reply with only DONE.", "prompt to send")
		model      = flag.String("model", "glm-5.2-ioa", "model id")
		cli        = flag.String("cli", "codebuddy", "codebuddy executable")
		configSrc  = flag.String("config-src", "", "source config dir to copy credentials from (default ~/.codebuddy)")
		withMCP    = flag.Bool("with-mcp", false, "copy mcp.json into isolated config dir (default: omit for clean baseline)")
		outDir     = flag.String("out", "tests/probe/fixtures", "directory to write captured frames")
		timeout    = flag.Duration("timeout", 4*time.Minute, "overall timeout")
		label      = flag.String("label", "", "fixture filename label (default derived from mode)")
		sessionCWD = flag.String("session-cwd", "", "ACP session/new cwd (default: process cwd); useful for CWD divergence probes")
		mcpCommand = flag.String("mcp-command", "", "absolute command for a controlled stdio MCP server")
		skillName  = flag.String("skill-name", "", "create a controlled skill with this runtime name")
		cliArgs    stringList
	)
	flag.Var(&cliArgs, "cli-arg", "additional CodeBuddy CLI argument; may be repeated")
	flag.Parse()

	if *label == "" {
		*label = *mode
	}
	if err := run(*mode, *prompt, *model, *cli, *configSrc, *outDir, *label, *withMCP, *sessionCWD, *mcpCommand, *skillName, cliArgs, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "probe: %v\n", err)
		os.Exit(1)
	}
}

func run(mode, prompt, model, cli, configSrc, outDir, label string, withMCP bool, sessionCWD, mcpCommand, skillName string, cliArgs []string, timeout time.Duration) error {
	cwd, err := os.MkdirTemp("", "probe-cwd-")
	if err != nil {
		return err
	}
	configDir, err := isolatedConfigDir(configSrc, withMCP)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if sessionCWD == "" {
		sessionCWD = cwd
	}
	if mcpCommand != "" {
		if err := writeMCPConfig(configDir, mcpCommand); err != nil {
			return err
		}
	}
	if skillName != "" {
		if err := writeSkill(configDir, skillName); err != nil {
			return err
		}
	}
	version := cliVersion(cli)
	rec, err := newRecorder(outDir, label, version, cwd, sessionCWD, configDir)
	if err != nil {
		return err
	}
	defer rec.close()

	fmt.Fprintf(os.Stderr, "probe: mode=%s cli=%s version=%s process-cwd=%s session-cwd=%s config=%s withMCP=%v\n", mode, cli, version, cwd, sessionCWD, configDir, withMCP)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	env := append(os.Environ(), "CODEBUDDY_CONFIG_DIR="+configDir)

	switch mode {
	case "acp":
		return runACP(ctx, cli, env, cwd, sessionCWD, prompt, model, cliArgs, rec)
	case "headless":
		return runHeadless(ctx, cli, env, cwd, prompt, model, cliArgs, rec)
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
}

func writeMCPConfig(configDir, command string) error {
	raw, err := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"codebuddy-driver-test-mcp": map[string]any{"command": command},
		},
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, "mcp.json"), raw, 0o600)
}

func writeSkill(configDir, name string) error {
	dir := filepath.Join(configDir, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: Return the CodeBuddy driver skill marker when explicitly invoked.\n---\n\nWhen asked to use this skill, reply exactly CODEBUDDY_DRIVER_SKILL_MARKER.\n", name)
	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600)
}

// ---- recorder ----------------------------------------------------------

type recorder struct {
	f   *os.File
	mu  sync.Mutex
	seq int
}

func newRecorder(outDir, label, version, processCWD, sessionCWD, configDir string) (*recorder, error) {
	path := filepath.Join(outDir, fmt.Sprintf("%s.jsonl", label))
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	r := &recorder{f: f}
	r.meta(map[string]any{
		"kind":       "meta",
		"label":      label,
		"cliVersion": version,
		"processCWD": processCWD,
		"sessionCWD": sessionCWD,
		"configDir":  configDir,
		"capturedAt": time.Now().UTC().Format(time.RFC3339),
	})
	fmt.Fprintf(os.Stderr, "probe: writing %s\n", path)
	return r, nil
}

func (r *recorder) meta(m map[string]any) {
	b, _ := json.Marshal(m)
	r.writeLine(b)
}

// record 记录一条方向化的帧。dir 为 "send"/"recv"/"note"。
func (r *recorder) record(dir string, raw []byte) {
	r.mu.Lock()
	r.seq++
	seq := r.seq
	r.mu.Unlock()
	entry := map[string]any{"kind": "frame", "seq": seq, "dir": dir, "ts": time.Now().UTC().Format(time.RFC3339Nano)}
	// 尽量把原始帧作为结构体嵌入，失败则退化为字符串。
	var parsed any
	if json.Unmarshal(raw, &parsed) == nil {
		entry["frame"] = parsed
	} else {
		entry["text"] = string(raw)
	}
	b, _ := json.Marshal(entry)
	r.writeLine(b)
	fmt.Fprintf(os.Stderr, "  [%s] %s\n", dir, truncate(string(raw), 400))
}

func (r *recorder) writeLine(b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.f.Write(append(b, '\n'))
}

func (r *recorder) close() { _ = r.f.Close() }

// ---- ACP flow ----------------------------------------------------------

func runACP(ctx context.Context, cli string, env []string, processCWD, sessionCWD, prompt, model string, cliArgs []string, rec *recorder) error {
	args := append([]string{"--acp", "--acp-transport", "stdio"}, cliArgs...)
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Dir = processCWD
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	send := func(id int, method string, params any) {
		msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
		b, _ := json.Marshal(msg)
		rec.record("send", b)
		_, _ = stdin.Write(append(b, '\n'))
	}
	sendResult := func(id any, result any) {
		msg := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
		b, _ := json.Marshal(msg)
		rec.record("send", b)
		_, _ = stdin.Write(append(b, '\n'))
	}

	type rpcMsg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	lines := make(chan []byte, 64)
	go func() {
		defer close(lines)
		for scanner.Scan() {
			b := append([]byte(nil), scanner.Bytes()...)
			if len(strings.TrimSpace(string(b))) == 0 {
				continue
			}
			lines <- b
		}
	}()

	// 读取直到匹配到某个 id 的 response；期间处理 server->client 的请求与通知。
	var sessionID string
	waitFor := func(wantID int) (*rpcMsg, error) {
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case b, ok := <-lines:
				if !ok {
					return nil, io.EOF
				}
				rec.record("recv", b)
				var m rpcMsg
				if err := json.Unmarshal(b, &m); err != nil {
					continue
				}
				// server->client 请求（带 method 且带 id）：request_permission 等。
				if m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null" {
					handleServerRequest(m.Method, m.Params, m.ID, sendResult, rec)
					continue
				}
				// 通知（带 method 不带 id）：session/update 等。
				if m.Method != "" {
					continue
				}
				// response：匹配 id。
				if idInt, ok := asInt(m.ID); ok && idInt == wantID {
					return &m, nil
				}
			}
		}
	}

	// 1. initialize
	send(1, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}},
	})
	if _, err := waitFor(1); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	// 2. session/new
	send(2, "session/new", map[string]any{"cwd": sessionCWD, "mcpServers": []any{}})
	newResp, err := waitFor(2)
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	var newRes struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(newResp.Result, &newRes)
	sessionID = newRes.SessionID
	rec.meta(map[string]any{"kind": "note", "note": "sessionId", "value": sessionID})

	// 3. session/prompt
	send(3, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]any{{"type": "text", "text": prompt}},
	})
	promptResp, err := waitFor(3)
	if err != nil {
		return fmt.Errorf("session/prompt: %w", err)
	}
	rec.meta(map[string]any{"kind": "note", "note": "promptResult", "value": json.RawMessage(promptResp.Result)})
	for _, dir := range []string{processCWD, sessionCWD} {
		path := filepath.Join(dir, "hello.txt")
		data, readErr := os.ReadFile(path)
		rec.meta(map[string]any{
			"kind":   "note",
			"note":   "hello.txt",
			"path":   path,
			"exists": readErr == nil,
			"content": func() string {
				if readErr != nil {
					return ""
				}
				return string(data)
			}(),
		})
	}

	_ = stdin.Close()
	return nil
}

// handleServerRequest 应答 CLI 反向发起的请求。对 request_permission 选择首个
// allow 类选项（approve），其余请求返回空 result 兜底。
func handleServerRequest(method string, params, id json.RawMessage, sendResult func(any, any), rec *recorder) {
	var rawID any
	_ = json.Unmarshal(id, &rawID)
	if !strings.Contains(method, "request_permission") {
		sendResult(rawID, map[string]any{})
		return
	}
	var p struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
			Name     string `json:"name"`
		} `json:"options"`
	}
	_ = json.Unmarshal(params, &p)
	chosen := ""
	for _, o := range p.Options {
		if strings.HasPrefix(strings.ToLower(o.Kind), "allow") || strings.Contains(strings.ToLower(o.OptionID), "allow") {
			chosen = o.OptionID
			break
		}
	}
	if chosen == "" && len(p.Options) > 0 {
		chosen = p.Options[0].OptionID
	}
	rec.meta(map[string]any{"kind": "note", "note": "permission.chosen", "value": chosen})
	sendResult(rawID, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": chosen}})
}

// ---- headless flow -----------------------------------------------------

func runHeadless(ctx context.Context, cli string, env []string, cwd, prompt, model string, cliArgs []string, rec *recorder) error {
	args := []string{"--print", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, cliArgs...)
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Dir = cwd
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	rec.meta(map[string]any{"kind": "note", "note": "args", "value": args})
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		b := append([]byte(nil), scanner.Bytes()...)
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		rec.record("recv", b)
	}
	return cmd.Wait()
}

// ---- helpers -----------------------------------------------------------

func isolatedConfigDir(src string, withMCP bool) (string, error) {
	dir, err := os.MkdirTemp("", "probe-config-")
	if err != nil {
		return "", err
	}
	if src == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return dir, nil
		}
		src = filepath.Join(home, ".codebuddy")
	}
	names := []string{".credentials.json", "credentials.json", "settings.json"}
	if withMCP {
		names = append(names, "mcp.json")
	}
	for _, name := range names {
		data, rerr := os.ReadFile(filepath.Join(src, name))
		if rerr != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(dir, name), data, 0o600)
	}
	return dir, nil
}

func cliVersion(cli string) string {
	out, err := exec.Command(cli, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func asInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	return 0, false
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, " ") }

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}
