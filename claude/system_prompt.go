package claude

import (
	"context"
	"errors"
	"runtime"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/processx"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
)

const appendSystemPromptFingerprintKey = "append_system_prompt_fingerprint"

func validateClaudeAppend(cfg Config, text string) error {
	if err := systemprompt.Validate(DriverType, text); err != nil {
		return err
	}
	for _, arg := range cfg.ExtraArgs {
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "--system-prompt", "--system-prompt-file", "--append-system-prompt", "--append-system-prompt-file":
			return &driver.SystemPromptUnsupportedError{Driver: DriverType, Reason: "conflicting_extra_args"}
		}
	}
	return nil
}

// prepareClaudeAppend owns the carrier until the actual process has exited.
// Every spawn uses a fresh file; reuse never depends on a random path.
func prepareClaudeAppend(ctx context.Context, command string, args []string, text string) (_ []string, _ *systemprompt.File, resultErr error) {
	file, err := systemprompt.Materialize(ctx, text)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, file.Close())
		}
	}()
	args = append([]string(nil), args...)
	if file != nil {
		args = append(args, "--append-system-prompt-file", file.Path())
	}
	preparedCommand, preparedArgs, err := processx.PrepareCommand(command, args)
	if err != nil {
		return nil, nil, err
	}
	if err = systemprompt.ValidateCommandLine(DriverType, preparedCommand, preparedArgs, runtime.GOOS); err != nil {
		return nil, nil, err
	}
	if err = file.Verify(ctx); err != nil {
		return nil, nil, err
	}
	return args, file, nil
}

// Preparation failures are fail-closed, never a transport replay invitation.
type appendPreparationError struct{ cause error }

func (e *appendPreparationError) Error() string { return e.cause.Error() }
func (e *appendPreparationError) Unwrap() error { return e.cause }
