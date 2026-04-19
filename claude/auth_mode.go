package claude

import (
	"regexp"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/configprobe"
)

const (
	defaultClaudeModel        = "claude-sonnet-4"
	defaultClaudeBedrockModel = "us.anthropic.claude-sonnet-4-5-20250929-v2:0"
)

var bedrockModelPattern = regexp.MustCompile(`^\w+\.anthropic\.`)

func claudeModels() []agentadaptor.ModelInfo {
	return cloneModelInfos([]agentadaptor.ModelInfo{
		{ID: defaultClaudeModel, Label: defaultClaudeModel},
		{ID: "claude-opus-4", Label: "claude-opus-4"},
	})
}

func claudeBedrockModels() []agentadaptor.ModelInfo {
	return cloneModelInfos([]agentadaptor.ModelInfo{
		{ID: "us.anthropic.claude-opus-4-6-v1", Label: "Bedrock Opus 4.6"},
		{ID: defaultClaudeBedrockModel, Label: "Bedrock Sonnet 4.5"},
		{ID: "us.anthropic.claude-haiku-4-5-20251001-v1:0", Label: "Bedrock Haiku 4.5"},
	})
}

func claudeModelsForBindings(bindings []agentadaptor.EnvBinding) []agentadaptor.ModelInfo {
	if claudeBedrockEnabled(bindings) {
		return claudeBedrockModels()
	}
	return claudeModels()
}

func claudeDefaultModel(bindings []agentadaptor.EnvBinding) string {
	if claudeBedrockEnabled(bindings) {
		return defaultClaudeBedrockModel
	}
	return defaultClaudeModel
}

func claudeBedrockEnabled(bindings []agentadaptor.EnvBinding) bool {
	if enabled, _ := adapterutil.ResolvedTruthyEnv(bindings, "CLAUDE_CODE_USE_BEDROCK"); enabled {
		return true
	}
	_, source := adapterutil.ResolvedEnvValue(bindings, "ANTHROPIC_BEDROCK_BASE_URL")
	return source != ""
}

func isBedrockModelID(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	return bedrockModelPattern.MatchString(model) || strings.HasPrefix(model, "arn:aws:bedrock:")
}

func claudeRequestedModelFlag(config agentadaptor.ClaudeConfig) string {
	model := strings.TrimSpace(config.Model)
	if model == "" {
		return ""
	}
	// Bedrock rejects Anthropic short IDs such as claude-sonnet-4, so in that
	// mode we only forward Bedrock-native identifiers and otherwise let the CLI
	// fall back to its own configured default.
	if claudeBedrockEnabled(config.Env) && !isBedrockModelID(model) {
		return ""
	}
	return model
}

func claudeConfigFileModel(bindings []agentadaptor.EnvBinding) (string, bool, error) {
	for _, candidate := range claudeConfigCandidates(bindings) {
		model, ok, err := configprobe.ReadTopLevelJSONString(candidate, "model")
		if err != nil {
			if isNotExist(err) {
				continue
			}
			return "", false, err
		}
		if ok {
			return model, true, nil
		}
	}
	return "", false, nil
}

func detectClaudeEffectiveModel(config agentadaptor.ClaudeConfig) (*agentadaptor.DetectedModel, error) {
	if model := claudeRequestedModelFlag(config); model != "" {
		return &agentadaptor.DetectedModel{
			Model:      model,
			Provider:   "anthropic",
			Source:     "binding_config",
			Candidates: []string{model},
		}, nil
	}
	// When a binding model is intentionally ignored in Bedrock mode, fall back
	// to the operator's local Claude config so admin surfaces stay truthful.
	model, ok, err := claudeConfigFileModel(effectiveClaudeBindings(config.CommonConfig))
	if err != nil || !ok {
		return nil, err
	}
	return &agentadaptor.DetectedModel{
		Model:      model,
		Provider:   "anthropic",
		Source:     "config_file",
		Candidates: []string{model},
	}, nil
}

func hydrateClaudeConfigSchema(config agentadaptor.ClaudeConfig) *agentadaptor.ConfigSchema {
	schema := cloneClaudeConfigSchema(adapter{}.Descriptor().ConfigSchema)
	if schema == nil {
		return nil
	}
	bindings := effectiveClaudeBindings(config.CommonConfig)
	models := claudeModelsForBindings(bindings)
	for i := range schema.Fields {
		if schema.Fields[i].Name != "model" {
			continue
		}
		schema.Fields[i].Options = modelOptions(models)
		schema.Fields[i].Default = claudeDefaultModel(bindings)
	}
	return schema
}

func cloneClaudeConfigSchema(schema *agentadaptor.ConfigSchema) *agentadaptor.ConfigSchema {
	if schema == nil {
		return nil
	}
	out := &agentadaptor.ConfigSchema{
		Fields: make([]agentadaptor.ConfigField, 0, len(schema.Fields)),
	}
	for _, field := range schema.Fields {
		copyField := field
		copyField.Options = append([]agentadaptor.ConfigOption(nil), field.Options...)
		if len(field.Meta) > 0 {
			copyField.Meta = make(map[string]string, len(field.Meta))
			for key, value := range field.Meta {
				copyField.Meta[key] = value
			}
		}
		out.Fields = append(out.Fields, copyField)
	}
	return out
}

func cloneModelInfos(models []agentadaptor.ModelInfo) []agentadaptor.ModelInfo {
	return append([]agentadaptor.ModelInfo(nil), models...)
}
