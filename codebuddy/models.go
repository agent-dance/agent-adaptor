package codebuddy

import (
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

const defaultModel = "claude-sonnet-5"

// models returns a representative subset of CodeBuddy-hosted models observed on
// the stream-json system.init response. CodeBuddy proxies many providers; this list is
// used for the config schema select field and admin surfaces. It is not
// exhaustive and the CLI accepts any id it recognises.
func models() []driver.ModelInfo {
	return cloneModelInfos([]driver.ModelInfo{
		{ID: "claude-sonnet-5", Label: "Claude-Sonnet-5"},
		{ID: "claude-opus-4.8", Label: "Claude-Opus-4.8"},
		{ID: "claude-haiku-4.5", Label: "Claude-Haiku-4.5"},
		{ID: "gemini-3.1-pro", Label: "Gemini-3.1-Pro"},
		{ID: "gpt-5.5", Label: "GPT-5.5"},
		{ID: "gpt-5.3-codex", Label: "GPT-5.3-Codex"},
	})
}

func modelOptions(list []driver.ModelInfo) []driver.ConfigOption {
	if len(list) == 0 {
		return nil
	}
	options := make([]driver.ConfigOption, 0, len(list))
	for _, model := range list {
		options = append(options, driver.ConfigOption{Value: model.ID, Label: model.Label})
	}
	return options
}

func requestedModelFlag(cfg Config) string {
	return strings.TrimSpace(cfg.Model)
}

func detectEffectiveModel(cfg Config) *driver.DetectedModel {
	if model := requestedModelFlag(cfg); model != "" {
		return &driver.DetectedModel{
			Model:      model,
			Provider:   "codebuddy",
			Source:     "binding_config",
			Candidates: []string{model},
		}
	}
	return nil
}

func cloneModelInfos(list []driver.ModelInfo) []driver.ModelInfo {
	return append([]driver.ModelInfo(nil), list...)
}
