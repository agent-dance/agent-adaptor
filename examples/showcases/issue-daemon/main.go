// Command issue-daemon exercises multica-style messaging against Codex or an
// offline scripted Driver, reporting every acceptance check as JSON.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"time"
)

type settings struct {
	agent, effort, reportPath string
	timeout                   time.Duration
	keep                      bool
}

func main() { os.Exit(cli()) }
func cli() int {
	var cfg settings
	flag.StringVar(&cfg.agent, "agent", "fake", "codex or fake (Codex performs paid local CLI calls)")
	flag.StringVar(&cfg.effort, "effort", "low", "Codex reasoning effort; must be low")
	flag.StringVar(&cfg.reportPath, "report", "report.json", "JSON report path, or - for stdout")
	flag.DurationVar(&cfg.timeout, "timeout", 3*time.Minute, "deadline for each independent scenario group")
	flag.BoolVar(&cfg.keep, "keep-workspaces", false, "retain temporary workspaces for inspection")
	flag.Parse()
	if cfg.agent != "fake" && cfg.agent != "codex" {
		fmt.Fprintln(os.Stderr, "-agent must be codex or fake")
		return 2
	}
	if cfg.effort != "low" || cfg.timeout <= 0 {
		fmt.Fprintln(os.Stderr, "-effort must be low and -timeout must be positive")
		return 2
	}
	if cfg.agent == "codex" {
		if _, err := exec.LookPath("codex"); err != nil {
			fmt.Fprintln(os.Stderr, "issue-daemon: codex CLI is missing from PATH; install/authenticate it or use -agent=fake")
			return 2
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	r := runScenario(ctx, cfg)
	var err error
	if cfg.reportPath == "-" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(r)
	} else {
		var b []byte
		b, err = json.MarshalIndent(r, "", "  ")
		if err == nil {
			err = os.WriteFile(cfg.reportPath, append(b, '\n'), 0600)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		return 2
	}
	fmt.Fprintf(os.Stderr, "issue-daemon: %d steps, passed=%v, report=%s\n", len(r.Steps), r.Passed, cfg.reportPath)
	if !r.Passed {
		return 1
	}
	return 0
}
