package claude

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

const (
	claudeContextWindowEnv = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"
	claudeAutoCompactEnv   = "CLAUDE_CODE_AUTO_COMPACT_WINDOW"
	claudeMaxExactTokens   = int64(1<<53 - 1)
)

// These bounds are the native autoCompactWindow range, before Claude caps the
// window to the model's capacity and subtracts the summary buffer. Rejecting
// out-of-range values avoids accepting an override the CLI silently clamps.
func validateClaudeContextConfig(cfg Config, runtimeEnv []driver.EnvBinding) error {
	if cfg.ContextWindowTokens < 0 || cfg.ContextWindowTokens > claudeMaxExactTokens {
		return invalidClaudeContext("ContextWindowTokens must be zero or a positive exact JavaScript integer")
	}
	if n := cfg.AutoCompactWindowTokens; n != 0 && (n < 100000 || n > 1000000) {
		return invalidClaudeContext("AutoCompactWindowTokens must be zero or between 100000 and 1000000")
	}
	for _, bindings := range [][]driver.EnvBinding{cfg.Env, runtimeEnv} {
		for _, binding := range bindings {
			if cfg.ContextWindowTokens != 0 && claudeContextEnvNameEqual(binding.Name, claudeContextWindowEnv) {
				return invalidClaudeContext("ContextWindowTokens conflicts with an explicit CLAUDE_CODE_MAX_CONTEXT_TOKENS environment binding")
			}
			if cfg.AutoCompactWindowTokens != 0 && claudeContextEnvNameEqual(binding.Name, claudeAutoCompactEnv) {
				return invalidClaudeContext("AutoCompactWindowTokens conflicts with an explicit CLAUDE_CODE_AUTO_COMPACT_WINDOW environment binding")
			}
		}
	}
	return nil
}

func invalidClaudeContext(reason string) error {
	return &driver.InvalidDriverConfigError{Driver: DriverType, Cause: errors.New(reason)}
}

func claudeContextEnvNameEqual(a, b string) bool {
	return a == b || runtime.GOOS == "windows" && strings.EqualFold(a, b)
}

// The caller has validated both constructor and runtime bindings before any
// profile preparation. The same resolved environment reaches one-shot and
// persistent execution and therefore also participates in the process signature.
func appendClaudeContextEnv(env []driver.EnvBinding, cfg Config) []driver.EnvBinding {
	if cfg.ContextWindowTokens != 0 {
		env = appendClaudeContextBinding(env, claudeContextWindowEnv, cfg.ContextWindowTokens)
	}
	if cfg.AutoCompactWindowTokens != 0 {
		env = appendClaudeContextBinding(env, claudeAutoCompactEnv, cfg.AutoCompactWindowTokens)
	}
	return env
}

func appendClaudeContextBinding(env []driver.EnvBinding, name string, tokens int64) []driver.EnvBinding {
	value := strconv.FormatInt(tokens, 10)
	if runtime.GOOS == "windows" {
		env = appendClaudeContextEnvAliases(env, os.Environ(), name, value)
	}
	return append(env, driver.EnvBinding{Name: name, Value: value})
}

// The process helpers merge exact key spellings before os/exec performs its
// Windows case-insensitive deduplication. Override every ambient spelling so
// that either helper's ordering leaves the same typed value in the child.
func appendClaudeContextEnvAliases(env []driver.EnvBinding, ambient []string, name, value string) []driver.EnvBinding {
	for _, entry := range ambient {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key != name && strings.EqualFold(key, name) {
			env = append(env, driver.EnvBinding{Name: key, Value: value})
		}
	}
	return env
}
