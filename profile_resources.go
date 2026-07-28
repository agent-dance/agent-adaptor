package adaptor

import (
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/profile"
)

// profileResourcesToEngine is the sole consumer-vocabulary boundary for
// profile desired state. It keeps profile independent from both the Driver
// SPI and the internal engine while transferring ownership of all documented
// collection shapes to the Agent option.
func profileResourcesToEngine(resources profile.Resources) engine.ProfileResources {
	var mcpConfig *engine.MCPConfig
	if resources.MCP != nil {
		mcpConfig = &engine.MCPConfig{Servers: engine.CloneMCPServerSpecs(resources.MCP)}
	}
	return engine.ProfileResources{
		Skills:       engine.CloneSkillRefs(resources.Skills),
		MCP:          mcpConfig,
		Agents:       publicSubAgentsToDriver(resources.Agents),
		Hooks:        publicHooksToDriver(resources.Hooks),
		Instructions: publicInstructionsToDriver(resources.Instructions),
		Config:       publicConfigPatchesToDriver(resources.Config),
	}
}

func publicSubAgentsToDriver(values []profile.SubAgent) []driver.AgentSpec {
	if values == nil {
		return nil
	}
	out := make([]driver.AgentSpec, len(values))
	for i, value := range values {
		out[i] = driver.AgentSpec{
			Key:               value.Key,
			RuntimeName:       value.RuntimeName,
			Description:       value.Description,
			Instructions:      value.Instructions,
			SourcePath:        value.SourcePath,
			SourceFingerprint: value.SourceFingerprint,
			Model:             value.Model,
			ReasoningEffort:   value.ReasoningEffort,
			ToolPolicy:        publicToolPolicyToDriver(value.ToolPolicy),
			PermissionMode:    value.PermissionMode,
			SandboxMode:       value.SandboxMode,
			MCPServers:        cloneProfileStrings(value.MCPServers),
			Skills:            cloneProfileStrings(value.Skills),
			Hooks:             publicHooksToDriver(value.Hooks),
			Native:            cloneProfileAnyMap(value.Native),
			Metadata:          cloneProfileStringMap(value.Metadata),
		}
	}
	return out
}

func publicToolPolicyToDriver(value *profile.ToolPolicy) *driver.AgentToolPolicy {
	if value == nil {
		return nil
	}
	return &driver.AgentToolPolicy{
		Allow: cloneProfileStrings(value.Allow),
		Deny:  cloneProfileStrings(value.Deny),
	}
}

func publicHooksToDriver(values []profile.Hook) []driver.HookSpec {
	if values == nil {
		return nil
	}
	out := make([]driver.HookSpec, len(values))
	for i, value := range values {
		out[i] = driver.HookSpec{
			Key:   value.Key,
			Event: driver.HookEvent(value.Event),
			MatcherSpec: driver.HookMatcher{
				Subject: driver.HookMatcherSubject(value.MatcherSpec.Subject),
				Syntax:  driver.HookMatcherSyntax(value.MatcherSpec.Syntax),
				Pattern: value.MatcherSpec.Pattern,
			},
			Handler:       publicHookHandlerToDriver(value.Handler),
			Timeout:       value.Timeout,
			FailPolicy:    driver.HookFailPolicy(value.FailPolicy),
			StatusMessage: value.StatusMessage,
			Disabled:      value.Disabled,
			Native:        cloneProfileAnyMap(value.Native),
			Metadata:      cloneProfileStringMap(value.Metadata),
		}
	}
	return out
}

func publicHookHandlerToDriver(value profile.HookHandler) driver.HookHandler {
	return driver.HookHandler{
		Type:    driver.HookHandlerType(value.Type),
		Command: value.Command,
		Args:    cloneProfileStrings(value.Args),
		Env:     cloneProfileStringMap(value.Env),
		Prompt:  value.Prompt,
		URL:     value.URL,
		Server:  value.Server,
		Tool:    value.Tool,
		Input:   cloneProfileAnyMap(value.Input),
		Agent:   value.Agent,
	}
}

func publicInstructionsToDriver(value *profile.Instructions) *driver.InstructionsBundleRef {
	if value == nil {
		return nil
	}
	return &driver.InstructionsBundleRef{
		ID:          value.ID,
		Path:        value.Path,
		Content:     value.Content,
		Fingerprint: value.Fingerprint,
		Scope:       driver.InstructionScope(value.Scope),
		Mode:        driver.InstructionMode(value.Mode),
		Native:      cloneProfileAnyMap(value.Native),
	}
}

func publicConfigPatchesToDriver(values []profile.ConfigPatch) []driver.ProfileConfigPatch {
	if values == nil {
		return nil
	}
	out := make([]driver.ProfileConfigPatch, len(values))
	for i, value := range values {
		out[i] = driver.ProfileConfigPatch{
			Key:        value.Key,
			Capability: value.Capability,
			Values:     cloneProfileAnyMap(value.Values),
			Native:     publicNativeConfigPatchToDriver(value.Native),
		}
	}
	return out
}

func publicNativeConfigPatchToDriver(value *profile.NativeConfigPatch) *driver.NativeConfigPatch {
	if value == nil {
		return nil
	}
	return &driver.NativeConfigPatch{
		Provider: value.Provider,
		FileKind: driver.ProfileConfigFileKind(value.FileKind),
		Path:     value.Path,
		Section:  value.Section,
		Values:   cloneProfileAnyMap(value.Values),
	}
}

func cloneProfileStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneProfileStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneProfileAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = cloneProfileAny(value)
	}
	return out
}

func cloneProfileAny(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneProfileAnyMap(value)
	case []any:
		if value == nil {
			return []any(nil)
		}
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneProfileAny(item)
		}
		return out
	case map[string]string:
		return cloneProfileStringMap(value)
	case []string:
		return cloneProfileStrings(value)
	case []byte:
		return append([]byte(nil), value...)
	default:
		return value
	}
}
