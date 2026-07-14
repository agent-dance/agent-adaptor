package codebuddy

import (
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

const defaultModel = "claude-sonnet-5"

// models returns a representative subset of CodeBuddy-hosted models observed on
// the stream-json system.init response. CodeBuddy proxies many providers; this list is
// used for the config schema select field and admin surfaces. It is not
// exhaustive and the CLI accepts any id it recognises.
func models() []agentadaptor.ModelInfo {
	return cloneModelInfos([]agentadaptor.ModelInfo{
		{ID: "claude-sonnet-5", Label: "Claude-Sonnet-5"},
		{ID: "claude-opus-4.8", Label: "Claude-Opus-4.8"},
		{ID: "claude-haiku-4.5", Label: "Claude-Haiku-4.5"},
		{ID: "gemini-3.1-pro", Label: "Gemini-3.1-Pro"},
		{ID: "gpt-5.5", Label: "GPT-5.5"},
		{ID: "gpt-5.3-codex", Label: "GPT-5.3-Codex"},
	})
}

func modelOptions(list []agentadaptor.ModelInfo) []agentadaptor.ConfigOption {
	if len(list) == 0 {
		return nil
	}
	options := make([]agentadaptor.ConfigOption, 0, len(list))
	for _, model := range list {
		options = append(options, agentadaptor.ConfigOption{Value: model.ID, Label: model.Label})
	}
	return options
}

func requestedModelFlag(cfg agentadaptor.CodeBuddyConfig) string {
	return strings.TrimSpace(cfg.Model)
}

func detectEffectiveModel(cfg agentadaptor.CodeBuddyConfig) *agentadaptor.DetectedModel {
	if model := requestedModelFlag(cfg); model != "" {
		return &agentadaptor.DetectedModel{
			Model:      model,
			Provider:   "codebuddy",
			Source:     "binding_config",
			Candidates: []string{model},
		}
	}
	return nil
}

func cloneModelInfos(list []agentadaptor.ModelInfo) []agentadaptor.ModelInfo {
	return append([]agentadaptor.ModelInfo(nil), list...)
}
