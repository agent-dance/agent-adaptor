package codebuddy

import (
	"strconv"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// buildExecArgs assembles the headless CodeBuddy CLI arguments. The prompt is
// passed as a trailing positional argument by the caller, so it is not part of
// the returned slice. CodeBuddy's headless flags mirror Claude Code's.
func buildExecArgs(cfg agentadaptor.CodeBuddyConfig, req agentadaptor.DriverRunRequest, permMode agentadaptor.CodeBuddyPermissionMode) []string {
	nativeStructured := req.OutputSchema != nil && req.OutputSchema.Mode != agentadaptor.StructuredOutputPromptValidate
	args := []string{"--print"}
	if nativeStructured {
		args = append(args, "--output-format", "json", "--json-schema", string(req.OutputSchema.SchemaJSON))
	} else {
		args = append(args, "--output-format", "stream-json", "--verbose")
		if req.Streaming {
			args = append(args, "--include-partial-messages")
		}
	}

	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "--resume", req.Session.State.ResumeID)
	}
	if permMode != agentadaptor.CodeBuddyPermissionUnset {
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
	args = append(args, cfg.ExtraArgs...)
	return args
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
