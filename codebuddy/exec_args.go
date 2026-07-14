package codebuddy

import (
	"strconv"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// buildExecArgs assembles CodeBuddy CLI arguments. Batch prompts are passed as
// a trailing positional argument; control prompts are NDJSON stdin frames.
func buildExecArgs(cfg agentadaptor.CodeBuddyConfig, req agentadaptor.DriverRunRequest, permMode agentadaptor.CodeBuddyPermissionMode, interactive ...bool) []string {
	control := len(interactive) > 0 && interactive[0]
	nativeStructured := req.OutputSchema != nil && req.OutputSchema.Mode != agentadaptor.StructuredOutputPromptValidate
	args := make([]string, 0, 16)
	if control {
		args = append(args, "--input-format=stream-json", "--output-format=stream-json", "--verbose")
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

	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "--resume", req.Session.State.ResumeID)
	}
	if !control && permMode != agentadaptor.CodeBuddyPermissionUnset {
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
	if control {
		args = append(args, controlSafeExtraArgs(cfg.ExtraArgs)...)
	} else {
		args = append(args, cfg.ExtraArgs...)
	}
	return args
}

func controlSafeExtraArgs(extra []string) []string {
	blocked := map[string]bool{
		"--acp": true, "--acp-transport": true, "--print": true,
		"--setting-sources": true, "--input-format": true, "--output-format": true,
	}
	out := make([]string, 0, len(extra))
	for i := 0; i < len(extra); i++ {
		name := extra[i]
		base := name
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			base = name[:eq]
		}
		if !blocked[base] {
			out = append(out, name)
			continue
		}
		if !strings.Contains(name, "=") && i+1 < len(extra) {
			i++
		}
	}
	return out
}

func hasAnyArg(args []string, names ...string) bool {
	for _, arg := range args {
		for _, name := range names {
			if arg == name || (len(arg) > len(name) && arg[:len(name)+1] == name+"=") {
				return true
			}
		}
	}
	return false
}
