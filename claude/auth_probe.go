package claude

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/configprobe"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

type claudeCredentialInfo struct {
	AccessToken string
	Path        string
}

func effectiveClaudeBindings(config driver.CommonConfig, selection *driver.ProfileSelection) ([]driver.EnvBinding, error) {
	profile, err := resolveClaudeProfileWithOptions(config, selection, false)
	if err != nil {
		return nil, err
	}
	return skillruntime.WithBinding(config.Env, "CLAUDE_CONFIG_DIR", profile.Dir), nil
}

func effectiveClaudeBindingsNoInitialize(config driver.CommonConfig, selection *driver.ProfileSelection) ([]driver.EnvBinding, error) {
	profile, err := resolveClaudeProfileWithOptions(config, selection, true)
	if err != nil {
		return nil, err
	}
	return skillruntime.WithBinding(config.Env, "CLAUDE_CONFIG_DIR", profile.Dir), nil
}

func resolveClaudeConfigDir(bindings []driver.EnvBinding) string {
	if configured := skillruntime.ResolveBinding(bindings, "CLAUDE_CONFIG_DIR"); strings.TrimSpace(configured) != "" {
		return filepath.Clean(configured)
	}
	if configured := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); configured != "" {
		return filepath.Clean(configured)
	}
	return filepath.Join(skillruntime.ResolveHome(bindings), ".claude")
}

func resolveClaudeProfile(config driver.CommonConfig, selection *driver.ProfileSelection) (driver.AgentProfile, error) {
	return resolveClaudeProfileWithOptions(config, selection, false)
}

func resolveClaudeProfileWithOptions(config driver.CommonConfig, selection *driver.ProfileSelection, skipInitialize bool) (driver.AgentProfile, error) {
	resolution, err := skillruntime.ResolveProfile(skillruntime.ProfileResolveOptions{
		Bindings:         config.Env,
		Selection:        selection,
		EnvVar:           "CLAUDE_CONFIG_DIR",
		DefaultDir:       filepath.Join(skillruntime.ResolveHome(config.Env), ".claude"),
		NativeSharedDir:  filepath.Join(skillruntime.ResolveHome(config.Env), ".claude"),
		DedicatedSubdirs: []string{"skills"},
		SettingsFiles:    []string{"settings.json", "config.json", ".claude.json"},
		MCPFiles:         []string{"settings.json", "config.json"},
		SkillsDirs:       []string{"skills"},
		AuthFiles:        []string{".credentials.json", "credentials.json"},
		SkipInitialize:   skipInitialize,
	})
	if err != nil {
		return driver.AgentProfile{}, err
	}
	profile := resolution.Profile
	profile.DriverType = DriverType
	return profile, nil
}

func claudeProfile(config driver.CommonConfig, selection *driver.ProfileSelection) driver.AgentProfile {
	profile, err := resolveClaudeProfile(config, selection)
	if err != nil {
		return driver.AgentProfile{DriverType: DriverType, Supported: true, EnvVar: "CLAUDE_CONFIG_DIR", Error: err.Error()}
	}
	return profile
}

func claudeCredentialCandidates(bindings []driver.EnvBinding) []string {
	root := resolveClaudeConfigDir(bindings)
	return []string{
		filepath.Join(root, ".credentials.json"),
		filepath.Join(root, "credentials.json"),
	}
}

func claudeAuthChecks(bindings []driver.EnvBinding) []driver.EnvironmentCheck {
	checks := make([]driver.EnvironmentCheck, 0)
	if enabled, source := adapterutil.ResolvedTruthyEnv(bindings, "CLAUDE_CODE_USE_BEDROCK"); enabled {
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "claude_bedrock_auth",
			Level:   "info",
			Message: "AWS Bedrock auth is enabled for Claude.",
			Detail:  authSourceDetail(source),
		})
	} else if _, source := adapterutil.ResolvedEnvValue(bindings, "ANTHROPIC_BEDROCK_BASE_URL"); source != "" {
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "claude_bedrock_auth",
			Level:   "info",
			Message: "Claude is configured to use an Anthropic Bedrock base URL.",
			Detail:  authSourceDetail(source),
		})
	}

	if region, source := adapterutil.ResolvedEnvValue(bindings, "AWS_REGION"); source != "" {
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "claude_bedrock_region",
			Level:   "info",
			Message: "AWS region is configured for Bedrock-backed Claude runs.",
			Detail:  region,
		})
	} else if region, source := adapterutil.ResolvedEnvValue(bindings, "AWS_DEFAULT_REGION"); source != "" {
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "claude_bedrock_region",
			Level:   "info",
			Message: "AWS default region is configured for Bedrock-backed Claude runs.",
			Detail:  region,
		})
	}

	if _, source := adapterutil.ResolvedEnvValue(bindings, "ANTHROPIC_API_KEY"); source != "" {
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "anthropic_api_key_present",
			Level:   "info",
			Message: "ANTHROPIC_API_KEY is available for Claude authentication.",
			Detail:  authSourceDetail(source),
		})
	}

	credentials, err := readClaudeCredentialInfo(bindings)
	if err != nil {
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "claude_credentials_unreadable",
			Level:   "warn",
			Message: "Claude credentials file exists but could not be parsed.",
			Detail:  err.Error(),
			Hint:    "Run `claude login` again or remove the broken credentials file.",
		})
	} else if credentials != nil {
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "claude_credentials_present",
			Level:   "info",
			Message: "Claude OAuth credentials file is present.",
			Detail:  credentials.Path,
		})
	}

	if len(checks) == 0 {
		return []driver.EnvironmentCheck{{
			Code:    "claude_credentials_missing",
			Level:   "warn",
			Message: "No Claude credentials file, Bedrock config, or ANTHROPIC_API_KEY was found.",
			Hint:    "Run `claude auth login`, configure Bedrock env, set ANTHROPIC_API_KEY in CommonConfig.Env, or use a profile option or point CLAUDE_CONFIG_DIR at an existing Claude profile.",
		}}
	}
	return checks
}

func claudeModelCompatibilityChecks(config Config) []driver.EnvironmentCheck {
	model := strings.TrimSpace(config.Model)
	if model == "" || !claudeBedrockEnabled(config.Env) || isBedrockModelID(model) {
		return nil
	}
	return []driver.EnvironmentCheck{{
		Code:    "claude_binding_model_ignored",
		Level:   "warn",
		Message: "ClaudeConfig.Model uses an Anthropic API model id and will be ignored in Bedrock mode.",
		Detail:  model,
		Hint:    "Use a Bedrock-native model id such as us.anthropic.* or clear ClaudeConfig.Model so the Claude CLI can use its local default model.",
	}}
}

func readClaudeCredentialInfo(bindings []driver.EnvBinding) (*claudeCredentialInfo, error) {
	for _, candidate := range claudeCredentialCandidates(bindings) {
		payload, err := configprobe.ReadJSONObject(candidate)
		if err != nil {
			if isNotExist(err) {
				continue
			}
			return nil, err
		}
		if nestedString(payload, "claudeAiOauth", "accessToken") == "" {
			continue
		}
		return &claudeCredentialInfo{
			AccessToken: nestedString(payload, "claudeAiOauth", "accessToken"),
			Path:        candidate,
		}, nil
	}
	return nil, nil
}

func nestedString(payload map[string]any, keys ...string) string {
	var current any = payload
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		value, ok := object[key]
		if !ok {
			return ""
		}
		current = value
	}
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

func authSourceDetail(source string) string {
	switch source {
	case "binding_env":
		return "Detected in CommonConfig.Env."
	case "process_env":
		return "Detected in the current process environment."
	default:
		return ""
	}
}

func isNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}
