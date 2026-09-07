package codebuddy

import (
	"runtime"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/processx"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
)

const appendSystemPromptFingerprintKey = "append_system_prompt_fingerprint"

func validateAppendConfig(cfg Config) error {
	for _, arg := range cfg.ExtraArgs {
		name, _, _ := strings.Cut(arg, "=")
		switch name {
		case "--system-prompt", "--system-prompt-file", "--append-system-prompt", "--append-system-prompt-file":
			return &driver.SystemPromptUnsupportedError{Driver: DriverType, Reason: "conflicting_extra_args"}
		}
	}
	return nil
}

// Validate precisely the executable/argv that processx will launch, including
// Windows shim wrappers. Keep real args unchanged; diagnostic copies redact.
func validateAppendCommandIfSet(text, command string, args []string) error {
	if text == "" {
		return nil
	}
	command, args, err := processx.PrepareCommand(command, args)
	if err != nil {
		return err
	}
	return systemprompt.ValidateCommandLine(DriverType, command, args, runtime.GOOS)
}

func redactedAppendArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i := 0; i < len(out); i++ {
		if out[i] == "--append-system-prompt" && i+1 < len(out) {
			out[i+1] = "[redacted]"
			i++
		}
	}
	return out
}

// The process helper owns raw capture. This wrapper only changes its SDK-made
// invocation diagnostic; stdout/stderr and parsed provider payloads stay intact.
type appendDiagnosticSink struct{ driver.EventSink }

func (s appendDiagnosticSink) Emit(e driver.RunEvent) error {
	if e.Type == driver.RunEventInvocation {
		data := make(map[string]any, len(e.Data))
		for k, v := range e.Data {
			data[k] = v
		}
		if args, ok := data["args"].([]string); ok {
			data["args"] = redactedAppendArgs(args)
		}
		e.Data = data
	}
	return s.EventSink.Emit(e)
}

func checkpointAppend(checkpoint *driver.Checkpoint, text string) {
	if checkpoint == nil || checkpoint.State == nil || text == "" {
		return
	}
	if checkpoint.State.Data == nil {
		checkpoint.State.Data = map[string]string{}
	}
	checkpoint.State.Data[appendSystemPromptFingerprintKey] = systemprompt.Fingerprint(text)
}
