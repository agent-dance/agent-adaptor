// Command claudebench is a standalone, SDK-independent probe that measures the
// cost of the two ways to drive the Claude Code CLI across a multi-turn
// conversation:
//
//	restart    — spawn a fresh `claude` process per turn and resume context via
//	             --resume <session_id>. This mirrors what claude/driver.go does
//	             today: one process per Run.
//	persistent — spawn ONE `claude --input-format stream-json` process and feed
//	             each turn as an NDJSON user frame on the long-lived stdin. The
//	             conversation context stays in the live process; no --resume.
//
// The goal is to answer one question with real numbers: how much wall time do
// we pay per message for (a) process cold start and (b) --resume context
// rehydration, and how much of that the persistent model would save.
//
// It intentionally does NOT import the agent-adaptor SDK. It only shells out to
// the `claude` binary the same way the adapter does, so the timings reflect the
// CLI itself, not our wrapping.
//
// Usage:
//
//	go run ./scripts/claudebench -turns 5
//	go run ./scripts/claudebench -mode restart -turns 8
//	go run ./scripts/claudebench -model claude-sonnet-4-5 -turns 5
//
// Requires: a logged-in `claude` CLI on PATH.
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
	"strings"
	"time"
)

func main() {
	var (
		mode    = flag.String("mode", "both", "which model to measure: restart | persistent | both")
		turns   = flag.Int("turns", 5, "number of conversational turns to send")
		command = flag.String("command", "claude", "claude binary to invoke")
		model   = flag.String("model", "", "optional --model value")
		cwd     = flag.String("cwd", "", "working dir for the CLI (default: a fresh temp dir)")
		prompt  = flag.String("prompt", "Reply with only the number %d and nothing else.", "per-turn prompt template; %d is the turn index")
		verbose  = flag.Bool("v", false, "print raw NDJSON lines as they arrive")
		phase3   = flag.Bool("phase3", false, "probe: spawn with Phase 3 interactive flags and check whether type:result arrives per turn WITHOUT closing stdin")
		planMode = flag.Bool("planmode", false, "phase3: add --permission-mode plan to reliably trigger ExitPlanMode control_request")
	)
	flag.Parse()

	workdir := *cwd
	if workdir == "" {
		d, err := os.MkdirTemp("", "claudebench-")
		if err != nil {
			fatal("mktemp: %v", err)
		}
		defer os.RemoveAll(d)
		workdir = d
	}

	prompts := make([]string, *turns)
	for i := range prompts {
		prompts[i] = fmt.Sprintf(*prompt, i+1)
	}

	env := &benchEnv{command: *command, model: *model, cwd: workdir, verbose: *verbose, planMode: *planMode}

	if *phase3 {
		env.probePhase3(prompts)
		return
	}

	fmt.Printf("claudebench: command=%s model=%q cwd=%s turns=%d\n\n", *command, *model, workdir, *turns)

	var restart, persistent []turnStat
	if *mode == "restart" || *mode == "both" {
		restart = env.runRestart(prompts)
		report("restart (spawn per turn + --resume)", restart)
	}
	if *mode == "persistent" || *mode == "both" {
		persistent = env.runPersistent(prompts)
		report("persistent (one process, stream-json stdin)", persistent)
	}
	if len(restart) > 0 && len(persistent) > 0 {
		compare(restart, persistent)
	}
}

type benchEnv struct {
	command  string
	model    string
	cwd      string
	verbose  bool
	planMode bool
}

// turnStat captures the wall-time breakdown of a single conversational turn.
type turnStat struct {
	index    int
	boot     time.Duration // time until the first NDJSON frame (system init) — process readiness
	total    time.Duration // time until the turn's result frame — full turn latency
	firstTok time.Duration // time until the first assistant text delta (0 if none seen)
}

// ---- restart mode: one process per turn, --resume for continuity ----

func (e *benchEnv) runRestart(prompts []string) []turnStat {
	stats := make([]turnStat, 0, len(prompts))
	sessionID := ""
	for i, p := range prompts {
		args := []string{"--print", "--output-format", "stream-json", "--verbose", "-"}
		if sessionID != "" {
			args = append(args, "--resume", sessionID)
		}
		if e.model != "" {
			args = append(args, "--model", e.model)
		}
		st, sid := e.oneShot(i, args, p)
		if sid != "" {
			sessionID = sid
		}
		stats = append(stats, st)
	}
	return stats
}

// oneShot spawns claude, writes the prompt as plain text on stdin, closes
// stdin, and reads NDJSON until the result frame. Mirrors the Phase 1 path.
func (e *benchEnv) oneShot(index int, args []string, prompt string) (turnStat, string) {
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, e.command, args...)
	cmd.Dir = e.cwd
	cmd.Env = os.Environ()
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		fatal("restart turn %d: start: %v", index+1, err)
	}
	io.WriteString(stdin, prompt)
	stdin.Close()

	st, sid := e.readTurn(index, stdout, start)
	cmd.Wait()
	return st, sid
}

// ---- persistent mode: one long-lived process, NDJSON frames per turn ----

func (e *benchEnv) runPersistent(prompts []string) []turnStat {
	ctx := context.Background()
	args := []string{"--print", "--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}
	if e.model != "" {
		args = append(args, "--model", e.model)
	}
	cmd := exec.CommandContext(ctx, e.command, args...)
	cmd.Dir = e.cwd
	cmd.Env = os.Environ()
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr

	reader := bufio.NewReaderSize(stdout, 1<<20)

	if err := cmd.Start(); err != nil {
		fatal("persistent: start: %v", err)
	}

	stats := make([]turnStat, 0, len(prompts))
	for i, p := range prompts {
		frame, err := encodeUserFrame(p)
		if err != nil {
			fatal("persistent turn %d: encode: %v", i+1, err)
		}
		start := time.Now()
		if _, err := io.WriteString(stdin, frame); err != nil {
			fatal("persistent turn %d: write stdin: %v", i+1, err)
		}
		st := e.readTurnFromReader(i, reader, start)
		stats = append(stats, st)
	}
	stdin.Close()
	cmd.Wait()
	return stats
}

// encodeUserFrame matches claude/driver.go encodeInteractiveUserFrame.
func encodeUserFrame(prompt string) (string, error) {
	frame := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": prompt,
		},
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return "", err
	}
	return string(raw) + "\n", nil
}

// ---- shared NDJSON turn reader ----

func (e *benchEnv) readTurn(index int, stdout io.Reader, start time.Time) (turnStat, string) {
	return e.consume(index, bufio.NewReaderSize(stdout, 1<<20), start)
}

func (e *benchEnv) readTurnFromReader(index int, r *bufio.Reader, start time.Time) turnStat {
	st, _ := e.consume(index, r, start)
	return st
}

// consume reads NDJSON lines until the turn's `result` frame, recording the
// boot (first frame), first assistant text delta, and total latency.
func (e *benchEnv) consume(index int, r *bufio.Reader, start time.Time) (turnStat, string) {
	st := turnStat{index: index}
	sessionID := ""
	sawFirst := false
	for {
		line, err := r.ReadString('\n')
		if line = strings.TrimSpace(line); line != "" {
			if e.verbose {
				fmt.Printf("  [t%d] %s\n", index+1, line)
			}
			if !sawFirst {
				st.boot = time.Since(start)
				sawFirst = true
			}
			var frame map[string]any
			if json.Unmarshal([]byte(line), &frame) == nil {
				if sid, _ := frame["session_id"].(string); sid != "" && sessionID == "" {
					sessionID = sid
				}
				if st.firstTok == 0 && isAssistantText(frame) {
					st.firstTok = time.Since(start)
				}
				if t, _ := frame["type"].(string); t == "result" {
					st.total = time.Since(start)
					return st, sessionID
				}
			}
		}
		if err != nil {
			if st.total == 0 {
				st.total = time.Since(start)
			}
			return st, sessionID
		}
	}
}

func isAssistantText(frame map[string]any) bool {
	t, _ := frame["type"].(string)
	if t == "assistant" {
		return true
	}
	// streaming partial: {"type":"stream_event","event":{"type":"content_block_delta",...}}
	if t == "stream_event" {
		if ev, ok := frame["event"].(map[string]any); ok {
			et, _ := ev["type"].(string)
			return strings.Contains(et, "text") || strings.Contains(et, "delta")
		}
	}
	return false
}

// ---- reporting ----

func report(title string, stats []turnStat) {
	fmt.Printf("== %s ==\n", title)
	fmt.Printf("  %-6s %10s %12s %10s\n", "turn", "boot", "first-token", "total")
	var sumBoot, sumTotal time.Duration
	for _, s := range stats {
		fmt.Printf("  %-6d %10s %12s %10s\n", s.index+1, ms(s.boot), ms(s.firstTok), ms(s.total))
		sumBoot += s.boot
		sumTotal += s.total
	}
	n := time.Duration(len(stats))
	if n > 0 {
		fmt.Printf("  %-6s %10s %12s %10s\n", "avg", ms(sumBoot/n), "", ms(sumTotal/n))
	}
	fmt.Println()
}

func compare(restart, persistent []turnStat) {
	var rTotal, pTotal, rBoot, pBoot time.Duration
	for _, s := range restart {
		rTotal += s.total
		rBoot += s.boot
	}
	for _, s := range persistent {
		pTotal += s.total
		pBoot += s.boot
	}
	rn := time.Duration(len(restart))
	pn := time.Duration(len(persistent))
	fmt.Println("== comparison ==")
	fmt.Printf("  restart    avg boot=%s  avg total=%s\n", ms(rBoot/rn), ms(rTotal/rn))
	fmt.Printf("  persistent avg boot=%s  avg total=%s\n", ms(pBoot/pn), ms(pTotal/pn))
	saved := rTotal/rn - pTotal/pn
	fmt.Printf("  => persistent saves ~%s per turn on average (boot+resume overhead avoided)\n", ms(saved))
	// Persistent boot is ~0 after turn 1; restart pays boot+resume every turn.
	if len(restart) > 1 {
		var laterBoot time.Duration
		for _, s := range restart[1:] {
			laterBoot += s.boot
		}
		fmt.Printf("  restart per-turn cold-start (turns 2+) avg=%s\n", ms(laterBoot/time.Duration(len(restart)-1)))
	}
}

// p3scenario is one persistent-HITL turn: a prompt plus how the probe (acting
// as the host) answers any can_use_tool control_request the CLI raises.
type p3scenario struct {
	name    string
	prompt  string
	decide  string // "allow" | "deny" | "deny-interrupt" | "" (no request expected)
	wantAsk bool   // whether we expect a control_request this turn
}

// probePhase3 empirically settles every persistent-HITL question against a live
// claude process with the full Phase 3 flag set, NEVER closing stdin between
// turns. For each turn it feeds a user frame, answers control_request frames per
// the scenario's decision, and reports whether type:result arrives (turn
// boundary) and whether the process survives for the next turn.
func (e *benchEnv) probePhase3(_ []string) {
	args := []string{
		"--print", "--output-format", "stream-json", "--verbose",
		"--input-format", "stream-json",
		"--include-partial-messages", "--replay-user-messages",
		"--permission-prompt-tool", "stdio",
	}
	if e.planMode {
		args = append(args, "--permission-mode", "plan")
	}
	if e.model != "" {
		args = append(args, "--model", e.model)
	}
	fmt.Printf("phase3 probe: %s %s\ncwd=%s\n\n", e.command, strings.Join(args, " "), e.cwd)

	cmd := exec.Command(e.command, args...)
	cmd.Dir = e.cwd
	cmd.Env = os.Environ()
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fatal("phase3: start: %v", err)
	}
	// Single long-lived reader goroutine per process (mirrors persistent.go,
	// which keeps exactly one reader). Turns consume from this shared channel.
	reader := bufio.NewReaderSize(stdout, 1<<20)
	lines := make(chan map[string]any, 64)
	go func() {
		defer close(lines)
		for {
			s, err := reader.ReadString('\n')
			if t := strings.TrimSpace(s); t != "" {
				var m map[string]any
				if json.Unmarshal([]byte(t), &m) == nil {
					if e.verbose {
						fmt.Printf("  %s\n", t)
					}
					lines <- m
				}
			}
			if err != nil {
				return
			}
		}
	}()

	plan := func(topic string) string {
		return "Enter plan mode, design a two-step plan for " + topic + " (do not actually edit). " +
			"Call ExitPlanMode with the plan. Do not use any other tools."
	}
	scenarios := []p3scenario{
		{name: "plan-allow", prompt: plan("refactoring main.go"), decide: "allow", wantAsk: true},
		{name: "plan-deny-interrupt", prompt: plan("splitting handler.go"), decide: "deny-interrupt", wantAsk: true},
		{name: "survive-after-interrupt", prompt: "Reply with only: ALIVE", decide: "", wantAsk: false},
		{name: "plan-deny-soft", prompt: plan("renaming a variable in util.go"), decide: "deny", wantAsk: true},
	}

	for i, sc := range scenarios {
		frame, _ := encodeUserFrame(sc.prompt)
		start := time.Now()
		if _, err := io.WriteString(stdin, frame); err != nil {
			fmt.Printf("[%s] WRITE FAILED (process dead): %v => process did NOT survive\n", sc.name, err)
			break
		}
		r := e.readPhase3Turn(i+1, sc, lines, stdin, start)
		status := "no-result"
		if r.gotResult {
			status = "GOT result"
		}
		fmt.Printf("[%s] %s in %s | control_request=%v | stop_reason=%q | resultSubtype=%q\n",
			sc.name, status, ms(time.Since(start)), r.sawAsk, r.stop, r.resultSubtype)
		if !r.gotResult {
			fmt.Printf("   => turn did not reach result without stdin close; stopping.\n")
			break
		}
	}
	_ = stdin.Close()
	_ = cmd.Wait()
	fmt.Println("\nphase3 probe done.")
}

type p3result struct {
	gotResult     bool
	sawAsk        bool
	stop          string
	resultSubtype string
}

func (e *benchEnv) readPhase3Turn(turn int, sc p3scenario, lines <-chan map[string]any, stdin io.Writer, _ time.Time) p3result {
	r := p3result{}
	timeout := time.After(60 * time.Second)
	for {
		select {
		case <-timeout:
			fmt.Printf("   [t%d] TIMEOUT 60s without result (no stdin close)\n", turn)
			return r
		case m, ok := <-lines:
			if !ok {
				return r
			}
			ft, _ := m["type"].(string)
			if sr := extractStopReason(m); sr != "" {
				r.stop = sr
			}
			if ft == "control_request" {
				r.sawAsk = true
				respondControl(stdin, m, sc.decide)
				continue
			}
			if ft == "result" {
				r.gotResult = true
				r.resultSubtype, _ = m["subtype"].(string)
				return r
			}
		}
	}
}

// respondControl writes a control_response mirroring the parser's shape
// (buildInteractiveControlResponse / writeInteractiveControlResponse).
func respondControl(stdin io.Writer, req map[string]any, decide string) {
	requestID, _ := req["request_id"].(string)
	inner, _ := req["request"].(map[string]any)
	toolUseID, _ := inner["tool_use_id"].(string)
	input, _ := inner["input"].(map[string]any)

	var resp map[string]any
	switch decide {
	case "allow":
		resp = map[string]any{"behavior": "allow", "updatedInput": input, "toolUseID": toolUseID}
	case "deny":
		resp = map[string]any{"behavior": "deny", "message": "Permission denied by probe."}
	case "deny-interrupt":
		resp = map[string]any{"behavior": "deny", "message": "Permission denied by probe.", "interrupt": true}
	default:
		resp = map[string]any{"behavior": "deny", "message": "unexpected request"}
	}
	frame := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   resp,
		},
	}
	raw, _ := json.Marshal(frame)
	_, _ = stdin.Write(append(raw, '\n'))
}

func extractStopReason(frame map[string]any) string {
	// stream_event → event.delta.stop_reason, or message.stop_reason
	if ev, ok := frame["event"].(map[string]any); ok {
		if d, ok := ev["delta"].(map[string]any); ok {
			if sr, _ := d["stop_reason"].(string); sr != "" {
				return sr
			}
		}
	}
	if m, ok := frame["message"].(map[string]any); ok {
		if sr, _ := m["stop_reason"].(string); sr != "" {
			return sr
		}
	}
	return ""
}

func ms(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "claudebench: "+format+"\n", args...)
	os.Exit(1)
}
