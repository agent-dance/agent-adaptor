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
		verbose = flag.Bool("v", false, "print raw NDJSON lines as they arrive")
		phase3  = flag.Bool("phase3", false, "probe: spawn with Phase 3 interactive flags and check whether type:result arrives per turn WITHOUT closing stdin")
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

	env := &benchEnv{command: *command, model: *model, cwd: workdir, verbose: *verbose}

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
	command string
	model   string
	cwd     string
	verbose bool
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

// probePhase3 answers the make-or-break question for persistent HITL: with the
// full Phase 3 interactive flag set, does the CLI emit a per-turn type:result
// WITHOUT closing stdin, and does it stay alive to accept the next user frame?
// It spawns once, sends each prompt as an NDJSON user frame, and reads until a
// result frame with a per-turn timeout — never closing stdin between turns.
func (e *benchEnv) probePhase3(prompts []string) {
	args := []string{
		"--print", "--output-format", "stream-json", "--verbose",
		"--input-format", "stream-json",
		"--include-partial-messages", "--replay-user-messages",
		"--permission-prompt-tool", "stdio",
	}
	if e.model != "" {
		args = append(args, "--model", e.model)
	}
	fmt.Printf("phase3 probe: %s %s\n\n", e.command, strings.Join(args, " "))

	cmd := exec.Command(e.command, args...)
	cmd.Dir = e.cwd
	cmd.Env = os.Environ()
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fatal("phase3: start: %v", err)
	}
	reader := bufio.NewReaderSize(stdout, 1<<20)

	for i, p := range prompts {
		frame, err := encodeUserFrame(p)
		if err != nil {
			fatal("phase3 turn %d: encode: %v", i+1, err)
		}
		start := time.Now()
		if _, err := io.WriteString(stdin, frame); err != nil {
			fmt.Printf("turn %d: WRITE FAILED (process dead?): %v\n", i+1, err)
			break
		}

		// Read until a result frame OR a per-turn timeout. Never close stdin.
		type res struct {
			gotResult bool
			lastType  string
			stop      string
		}
		ch := make(chan res, 1)
		go func() {
			r := res{}
			for {
				line, err := reader.ReadString('\n')
				if t := strings.TrimSpace(line); t != "" {
					if e.verbose {
						fmt.Printf("  [t%d] %s\n", i+1, t)
					}
					var frame map[string]any
					if json.Unmarshal([]byte(t), &frame) == nil {
						ft, _ := frame["type"].(string)
						if ft != "" {
							r.lastType = ft
						}
						if sr := extractStopReason(frame); sr != "" {
							r.stop = sr
						}
						if ft == "result" {
							r.gotResult = true
							ch <- r
							return
						}
					}
				}
				if err != nil {
					ch <- r
					return
				}
			}
		}()

		select {
		case r := <-ch:
			if r.gotResult {
				fmt.Printf("turn %d: GOT type:result WITHOUT stdin close in %s (last stop_reason=%q) => per-turn result works\n", i+1, ms(time.Since(start)), r.stop)
			} else {
				fmt.Printf("turn %d: stream ended without result (lastType=%q) — process likely exited\n", i+1, r.lastType)
			}
		case <-time.After(20 * time.Second):
			fmt.Printf("turn %d: NO result after 20s without stdin close (last stop_reason seen via -v) => result requires stdin close (need message_stop boundary)\n", i+1)
			_ = stdin.Close()
			_ = cmd.Wait()
			return
		}
	}
	_ = stdin.Close()
	_ = cmd.Wait()
	fmt.Println("\nphase3 probe done: process accepted all frames on one live stdin.")
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
