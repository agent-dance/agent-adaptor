// Command multica-routing exercises a small, explicit host routing contract
// inspired by Multica. It never starts a model without explicit CLI flags.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

type settings struct {
	agent, model, effort, report, output, home, command string
	timeout                                             time.Duration
	keep                                                bool
}

func main() { os.Exit(cli()) }

func cli() int {
	var cfg settings
	flag.StringVar(&cfg.agent, "agent", "", "required: codex; starts real model calls")
	flag.StringVar(&cfg.model, "model", "", "required: explicit model, e.g. gpt-6.1-sol")
	flag.StringVar(&cfg.effort, "effort", "", "required: explicit reasoning effort, e.g. xhigh")
	flag.StringVar(&cfg.report, "report", "", "required: JSON summary path")
	flag.StringVar(&cfg.output, "output", "", "audit directory (default: report path + .artifacts)")
	flag.StringVar(&cfg.home, "codex-home", os.Getenv("CODEX_HOME"), "approved source profile; settings copied and auth linked separately per Agent")
	flag.StringVar(&cfg.command, "codex-command", "codex", "Codex CLI executable")
	flag.DurationVar(&cfg.timeout, "timeout", 5*time.Minute, "deadline for each team execution")
	flag.BoolVar(&cfg.keep, "keep-workspaces", false, "retain disposable workspaces and linked-auth profiles")
	flag.Parse()
	if err := validateSettings(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "multica-routing:", err)
		return 2
	}
	if _, err := exec.LookPath(cfg.command); err != nil {
		fmt.Fprintln(os.Stderr, "multica-routing: Codex CLI not found")
		return 2
	}
	if cfg.output == "" {
		cfg.output = cfg.report + ".artifacts"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	report, secrets := runScenario(ctx, cfg)
	if err := writeJSON(cfg.report, report, secrets); err != nil {
		fmt.Fprintln(os.Stderr, "multica-routing: write report:", err)
		return 2
	}
	fmt.Fprintf(os.Stderr, "multica-routing: passed=%v, report=%s, audit=%s\n", report.Passed, cfg.report, cfg.output)
	if !report.Passed {
		return 1
	}
	return 0
}

func validateSettings(cfg settings) error {
	if cfg.agent != "codex" || cfg.model == "" || cfg.effort == "" || cfg.report == "" || cfg.report == "-" {
		return fmt.Errorf("explicit -agent=codex -model -effort -report are required; no model runs by default")
	}
	switch cfg.effort {
	case "low", "medium", "high", "xhigh":
	default:
		return fmt.Errorf("unsupported -effort")
	}
	if cfg.timeout <= 0 {
		return fmt.Errorf("-timeout must be positive")
	}
	if cfg.home == "" {
		return fmt.Errorf("set -codex-home or CODEX_HOME to an approved source profile")
	}
	info, err := os.Stat(cfg.home)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("source Codex profile is unavailable")
	}
	return nil
}

// These values are used only to redact audit files, never printed or included
// as configuration. Raw audit data otherwise remains complete.
func auditSecrets(home string) []string {
	var secrets []string
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		if value := os.Getenv(name); value != "" {
			secrets = append(secrets, value)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return secrets
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return secrets
	}
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, item := range value {
				lower := strings.ToLower(key)
				if text, ok := item.(string); ok && (strings.Contains(lower, "token") || strings.Contains(lower, "api_key")) && text != "" {
					secrets = append(secrets, text)
				} else {
					visit(item)
				}
			}
		case []any:
			for _, item := range value {
				visit(item)
			}
		}
	}
	visit(value)
	return secrets
}
