package codebuddy

import (
	"strconv"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

// buildExecArgs assembles CodeBuddy CLI arguments. Batch prompts are passed as
// a trailing positional argument; control prompts are NDJSON stdin frames.
func buildExecArgs(cfg Config, req driver.Request, permMode PermissionMode, interactive ...bool) []string {
	control := len(interactive) > 0 && interactive[0]
	nativeStructured := req.OutputSchema != nil && req.OutputSchema.Mode != driver.StructuredOutputPromptValidate
	args := make([]string, 0, 16)
	if control {
		args = append(args, "--input-format=stream-json", "--output-format=stream-json", "--verbose", "--include-partial-messages")
	} else if nativeStructured {
		args = append(args, "--print")
		args = append(args, "--output-format", "json", "--json-schema", string(req.OutputSchema.SchemaJSON))
	} else {
		args = append(args, "--print")
		args = append(args, "--output-format", "stream-json", "--verbose")
		if req.Streaming {
			args = append(args, "--include-partial-messages")
		}
	}

	if req.Session != nil && req.Session.Mode != driver.SessionFork && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "--resume", req.Session.State.ResumeID)
	}
	if !control && permMode != PermissionUnset {
		args = append(args, "--permission-mode", string(permMode))
	}
	if model := requestedModelFlag(cfg); model != "" {
		args = append(args, "--model", model)
	}
	if cfg.Effort != "" {
		args = append(args, "--effort", string(cfg.Effort))
	}
	if cfg.MaxTurnsPerRun > 0 {
		args = append(args, "--max-turns", strconv.Itoa(cfg.MaxTurnsPerRun))
	}
	args = append(args, codeBuddySafeExtraArgs(cfg.ExtraArgs, control)...)
	return args
}

// codeBuddySafeExtraArgs preserves provider-specific escape hatches while
// preventing construction-time arguments from replacing the resolved
// invocation's transport, output contract, session selector, model, limits,
// or permission policy. Filtering is unconditional: an inherited/empty call
// value must not let constructor ExtraArgs become a hidden nearer override.
func codeBuddySafeExtraArgs(extra []string, control bool) []string {
	blockedValues := map[string]bool{
		"--input-format": true, "--output-format": true, "--json-schema": true,
		"--resume": true, "-r": true, "--session-id": true,
		"--model": true, "--effort": true, "--max-turns": true,
		"--permission-mode": true, "--permission-prompt-tool": true,
	}
	blockedBooleans := map[string]bool{
		"--print": true, "-p": true, "--include-partial-messages": true,
		"--continue": true, "-c": true, "--fork-session": true,
		"--dangerously-skip-permissions": true, "-y": true,
	}
	if control {
		blockedValues["--acp-transport"] = true
		blockedValues["--setting-sources"] = true
		blockedBooleans["--acp"] = true
	}
	out := make([]string, 0, len(extra))
	for i := 0; i < len(extra); i++ {
		name := extra[i]
		base := name
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			base = name[:eq]
		}
		if !blockedValues[base] && !blockedBooleans[base] {
			out = append(out, name)
			continue
		}
		if blockedValues[base] && !strings.Contains(name, "=") && i+1 < len(extra) && !strings.HasPrefix(extra[i+1], "-") {
			i++
		}
	}
	return out
}
