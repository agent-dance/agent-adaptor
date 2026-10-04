package codex

import (
	"errors"
	"strconv"

	"github.com/agent-dance/agent-adaptor/driver"
)

func validateCodexContextConfig(cfg Config) error {
	if cfg.ContextWindowTokens < 0 || cfg.AutoCompactTokenLimit < 0 {
		return invalidCodexContext("context-window controls must be zero or positive")
	}
	if cfg.ContextWindowTokens == 0 && cfg.AutoCompactTokenLimit == 0 {
		return nil
	}
	return visitCodexConfigKeys(cfg.ExtraArgs, func(key string) error {
		if key == "model_context_window" && cfg.ContextWindowTokens != 0 {
			return invalidCodexContext("ContextWindowTokens conflicts with model_context_window in ExtraArgs")
		}
		if key == "model_auto_compact_token_limit" && cfg.AutoCompactTokenLimit != 0 {
			return invalidCodexContext("AutoCompactTokenLimit conflicts with model_auto_compact_token_limit in ExtraArgs")
		}
		return nil
	})
}

func invalidCodexContext(reason string) error {
	return &driver.InvalidDriverConfigError{Driver: DriverType, Cause: errors.New(reason)}
}

func codexContextArgs(cfg Config) ([]string, error) {
	if err := validateCodexContextConfig(cfg); err != nil {
		return nil, err
	}
	var args []string
	if cfg.ContextWindowTokens != 0 {
		args = append(args, "-c", "model_context_window="+strconv.FormatInt(cfg.ContextWindowTokens, 10))
	}
	if cfg.AutoCompactTokenLimit != 0 {
		args = append(args, "-c", "model_auto_compact_token_limit="+strconv.FormatInt(cfg.AutoCompactTokenLimit, 10))
	}
	return args, nil
}
