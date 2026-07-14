package codebuddy

import (
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/configprobe"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

// configEnvVar is CodeBuddy's config directory override.
const configEnvVar = "CODEBUDDY_CONFIG_DIR"

func effectiveBindings(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection) ([]agentadaptor.EnvBinding, error) {
	profile, err := resolveProfileWithOptions(config, selection, false)
	if err != nil {
		return nil, err
	}
	return skillruntime.WithBinding(config.Env, configEnvVar, profile.Dir), nil
}

func effectiveBindingsNoInitialize(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection) ([]agentadaptor.EnvBinding, error) {
	profile, err := resolveProfileWithOptions(config, selection, true)
	if err != nil {
		return nil, err
	}
	return skillruntime.WithBinding(config.Env, configEnvVar, profile.Dir), nil
}

func resolveConfigDir(bindings []agentadaptor.EnvBinding) string {
	if configured := skillruntime.ResolveBinding(bindings, configEnvVar); strings.TrimSpace(configured) != "" {
		return filepath.Clean(configured)
	}
	if configured := strings.TrimSpace(os.Getenv(configEnvVar)); configured != "" {
		return filepath.Clean(configured)
	}
	return filepath.Join(skillruntime.ResolveHome(bindings), ".codebuddy")
}

func canonicalSharedConfigDir(bindings []agentadaptor.EnvBinding) string {
	return filepath.Join(skillruntime.ResolveHome(bindings), ".codebuddy")
}

func resolveProfileWithOptions(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection, skipInitialize bool) (agentadaptor.AgentProfile, error) {
	resolution, err := skillruntime.ResolveProfile(skillruntime.ProfileResolveOptions{
		Bindings:         config.Env,
		Selection:        selection,
		EnvVar:           configEnvVar,
		DefaultDir:       filepath.Join(skillruntime.ResolveHome(config.Env), ".codebuddy"),
		NativeSharedDir:  filepath.Join(skillruntime.ResolveHome(config.Env), ".codebuddy"),
		DedicatedSubdirs: []string{"skills"},
		SettingsFiles:    []string{"settings.json"},
		MCPFiles:         []string{"mcp.json"},
		SkillsDirs:       []string{"skills"},
		AuthFiles:        []string{".credentials.json", "credentials.json"},
		SkipInitialize:   skipInitialize,
	})
	if err != nil {
		return agentadaptor.AgentProfile{}, err
	}
	profile := resolution.Profile
	profile.DriverType = DriverType
	return profile, nil
}

func resolveProfile(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection) agentadaptor.AgentProfile {
	profile, err := resolveProfileWithOptions(config, selection, false)
	if err != nil {
		return agentadaptor.AgentProfile{DriverType: DriverType, Supported: true, EnvVar: configEnvVar, Error: err.Error()}
	}
	return profile
}

func credentialCandidates(bindings []agentadaptor.EnvBinding) []string {
	root := resolveConfigDir(bindings)
	return []string{
		filepath.Join(root, ".credentials.json"),
		filepath.Join(root, "credentials.json"),
	}
}

func authChecks(bindings []agentadaptor.EnvBinding) []agentadaptor.EnvironmentCheck {
	checks := make([]agentadaptor.EnvironmentCheck, 0)
	for _, candidate := range credentialCandidates(bindings) {
		payload, err := configprobe.ReadJSONObject(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			checks = append(checks, agentadaptor.EnvironmentCheck{
				Code:    "codebuddy_credentials_unreadable",
				Level:   "warn",
				Message: "CodeBuddy credentials file exists but could not be parsed.",
				Detail:  err.Error(),
				Hint:    "Run `codebuddy` and log in again, or remove the broken credentials file.",
			})
			return checks
		}
		if len(payload) == 0 {
			continue
		}
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "codebuddy_credentials_present",
			Level:   "info",
			Message: "CodeBuddy credentials file is present.",
			Detail:  candidate,
		})
		return checks
	}

	if _, source := adapterutil.ResolvedEnvValue(bindings, "CODEBUDDY_API_KEY"); source != "" {
		return append(checks, agentadaptor.EnvironmentCheck{
			Code:    "codebuddy_api_key_present",
			Level:   "info",
			Message: "CODEBUDDY_API_KEY is available for CodeBuddy authentication.",
		})
	}

	return append(checks, agentadaptor.EnvironmentCheck{
		Code:    "codebuddy_credentials_missing",
		Level:   "warn",
		Message: "No CodeBuddy credentials file or CODEBUDDY_API_KEY was found.",
		Hint:    "Run `codebuddy` to log in, set CODEBUDDY_API_KEY in CommonConfig.Env, or point CODEBUDDY_CONFIG_DIR at an existing CodeBuddy profile.",
	})
}
