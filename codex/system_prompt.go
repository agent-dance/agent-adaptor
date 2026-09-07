package codex

import (
	"errors"
	"runtime"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/processx"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
	"github.com/pelletier/go-toml/v2"
)

const appendSystemPromptFingerprintKey = "append_system_prompt_fingerprint"

// Validate every override before profile I/O, including hidden defaults when
// the SDK append is empty. TOML decoding handles quoted/dotted keys faithfully.
func validateCodexPromptArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		base, value, inline := splitCodexArg(args[i])
		if base != "-c" && base != "--config" {
			continue
		}
		if !inline {
			i++
			if i >= len(args) {
				return invalidCodexConfigOverride()
			}
			value = args[i]
		}
		var decoded map[string]any
		if err := toml.Unmarshal([]byte(value), &decoded); err != nil || len(decoded) != 1 {
			return invalidCodexConfigOverride()
		}
		for key := range decoded {
			switch key {
			case "developer_instructions", "instructions", "base_instructions", "model_instructions_file", "experimental_instructions_file":
				return &driver.SystemPromptUnsupportedError{Driver: DriverType, Reason: "conflicting_extra_args"}
			}
		}
	}
	return nil
}

func invalidCodexConfigOverride() error {
	return &driver.InvalidDriverConfigError{Driver: DriverType, Cause: errors.New("ExtraArgs contains an invalid TOML config override")}
}

func validateCodexAppend(req driver.Request, cfg Config) error {
	if err := systemprompt.Validate(DriverType, req.AppendSystemPrompt); err != nil {
		return err
	}
	if err := validateCodexPromptArgs(cfg.ExtraArgs); err != nil {
		return err
	}
	if !usesCodexAppServer(req) {
		return systemprompt.ValidateInline(DriverType, req.AppendSystemPrompt)
	}
	return nil
}

func codexAppendArgs(text string) ([]string, error) {
	if text == "" {
		return nil, nil
	}
	value, err := systemprompt.TOMLString(text)
	if err != nil {
		return nil, err
	}
	return []string{"-c", "developer_instructions=" + value}, nil
}

func validateCodexCommand(command string, args []string) error {
	executable, finalArgs, err := processx.PrepareCommand(command, args)
	if err != nil {
		return err
	}
	return systemprompt.ValidateCommandLine(DriverType, executable, finalArgs, runtime.GOOS)
}

// Only SDK-generated invocation diagnostics are redacted; provider audit bytes
// and protocol entries continue through the original sink unchanged.
type codexPromptDiagnosticSink struct{ driver.EventSink }

func (s codexPromptDiagnosticSink) Emit(e driver.RunEvent) error {
	if e.Type == driver.RunEventInvocation {
		if args, ok := e.Data["args"].([]string); ok {
			data := make(map[string]any, len(e.Data))
			for k, v := range e.Data {
				data[k] = v
			}
			args = append([]string(nil), args...)
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "-c" && strings.HasPrefix(args[i+1], "developer_instructions=") {
					args[i+1] = "developer_instructions=<redacted>"
				}
			}
			data["args"] = args
			e.Data = data
		}
	}
	return s.EventSink.Emit(e)
}

// The early and final command checks use the same resolved argv construction.
func codexExecArgs(req driver.Request, cfg Config, schemaPath string) ([]string, error) {
	appendArgs, err := codexAppendArgs(req.AppendSystemPrompt)
	if err != nil {
		return nil, err
	}
	args := append(codexPolicyArgs(req.Policy), "exec", "--json")
	args = append(args, appendArgs...)
	if schemaPath != "" {
		args = append(args, "--output-schema", schemaPath)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+string(cfg.ReasoningEffort))
	}
	if cfg.FastMode {
		args = append(args, "-c", `service_tier="fast"`, "-c", "features.fast_mode=true")
	}
	args = append(args, filterCodexPolicyExtraArgs(cfg.ExtraArgs, req.Policy)...)
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "resume", req.Session.State.ResumeID, "-")
	} else {
		args = append(args, "-")
	}
	return args, nil
}
