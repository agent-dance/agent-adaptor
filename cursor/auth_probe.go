package cursor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/configprobe"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

type cursorAuthInfo struct {
	Path        string
	Email       string
	DisplayName string
	UserID      int
}

func effectiveCursorBindings(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection) ([]agentadaptor.EnvBinding, error) {
	profile, err := resolveCursorProfileWithOptions(config, selection, false)
	if err != nil {
		return nil, err
	}
	return skillruntime.WithBinding(config.Env, "CURSOR_HOME", profile.Dir), nil
}

func effectiveCursorBindingsNoInitialize(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection) ([]agentadaptor.EnvBinding, error) {
	profile, err := resolveCursorProfileWithOptions(config, selection, true)
	if err != nil {
		return nil, err
	}
	return skillruntime.WithBinding(config.Env, "CURSOR_HOME", profile.Dir), nil
}

func resolveCursorHome(bindings []agentadaptor.EnvBinding) string {
	if configured := skillruntime.ResolveBinding(bindings, "CURSOR_HOME"); strings.TrimSpace(configured) != "" {
		return filepath.Clean(configured)
	}
	if configured := strings.TrimSpace(os.Getenv("CURSOR_HOME")); configured != "" {
		return filepath.Clean(configured)
	}
	return filepath.Join(skillruntime.ResolveHome(bindings), ".cursor")
}

func resolveCursorProfile(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error) {
	return resolveCursorProfileWithOptions(config, selection, false)
}

func resolveCursorProfileWithOptions(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection, skipInitialize bool) (agentadaptor.AgentProfile, error) {
	resolution, err := skillruntime.ResolveProfile(skillruntime.ProfileResolveOptions{
		Bindings:         config.Env,
		Selection:        selection,
		EnvVar:           "CURSOR_HOME",
		DefaultDir:       filepath.Join(skillruntime.ResolveHome(config.Env), ".cursor"),
		NativeSharedDir:  filepath.Join(skillruntime.ResolveHome(config.Env), ".cursor"),
		DedicatedSubdirs: []string{"skills"},
		SettingsFiles:    []string{"config.json", "settings.json"},
		MCPFiles:         []string{"mcp.json"},
		SkillsDirs:       []string{"skills"},
		AuthFiles:        []string{"auth.json", "credentials.json"},
		SkipInitialize:   skipInitialize,
	})
	if err != nil {
		return agentadaptor.AgentProfile{}, err
	}
	profile := resolution.Profile
	profile.DriverType = DriverType
	return profile, nil
}

func cursorProfile(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection) agentadaptor.AgentProfile {
	profile, err := resolveCursorProfile(config, selection)
	if err != nil {
		return agentadaptor.AgentProfile{DriverType: DriverType, Supported: true, EnvVar: "CURSOR_HOME", Error: err.Error()}
	}
	return profile
}

func cursorAuthChecks(bindings []agentadaptor.EnvBinding) []agentadaptor.EnvironmentCheck {
	checks := make([]agentadaptor.EnvironmentCheck, 0, 2)
	if _, source := adapterutil.ResolvedEnvValue(bindings, "CURSOR_API_KEY"); source != "" {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "cursor_api_key_present",
			Level:   "info",
			Message: "CURSOR_API_KEY is available for Cursor authentication.",
			Detail:  authSourceDetail(source),
		})
	}
	if _, source := adapterutil.ResolvedEnvValue(bindings, "OPENAI_API_KEY"); source != "" {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "cursor_openai_api_key_present",
			Level:   "info",
			Message: "OPENAI_API_KEY is available for Cursor API-mode authentication.",
			Detail:  authSourceDetail(source),
		})
	}
	if len(checks) > 0 {
		return checks
	}

	auth, err := readCursorAuthInfo(bindings)
	if err != nil {
		return []agentadaptor.EnvironmentCheck{{
			Code:    "cursor_auth_unreadable",
			Level:   "warn",
			Message: "Cursor CLI auth state exists but could not be parsed.",
			Detail:  err.Error(),
			Hint:    "Run `agent login` again or remove the broken Cursor CLI config file.",
		}}
	}
	if auth == nil {
		return []agentadaptor.EnvironmentCheck{{
			Code:    "cursor_auth_missing",
			Level:   "warn",
			Message: "No Cursor CLI auth state, CURSOR_API_KEY, or OPENAI_API_KEY was found.",
			Hint:    "Run `agent login`, set CURSOR_API_KEY / OPENAI_API_KEY in CommonConfig.Env, or use a profile option or point CURSOR_HOME at an existing Cursor profile.",
		}}
	}

	checks = []agentadaptor.EnvironmentCheck{{
		Code:    "cursor_auth_present",
		Level:   "info",
		Message: "Cursor CLI auth state is present.",
		Detail:  auth.Path,
	}}
	if auth.Email != "" {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "cursor_auth_email",
			Level:   "info",
			Message: "Cursor auth identifies the logged-in account.",
			Detail:  auth.Email,
		})
	}
	if auth.DisplayName != "" {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "cursor_auth_display_name",
			Level:   "info",
			Message: "Cursor auth exposes the operator display name.",
			Detail:  auth.DisplayName,
		})
	}
	if auth.UserID > 0 {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "cursor_auth_user_id",
			Level:   "info",
			Message: "Cursor auth exposes the operator user id.",
			Detail:  strconv.Itoa(auth.UserID),
		})
	}
	return checks
}

func readCursorAuthInfo(bindings []agentadaptor.EnvBinding) (*cursorAuthInfo, error) {
	configPath := filepath.Join(resolveCursorHome(bindings), "cli-config.json")
	payload, err := configprobe.ReadJSONObject(configPath)
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	authInfo, ok := payload["authInfo"].(map[string]any)
	if !ok {
		return nil, nil
	}
	info := &cursorAuthInfo{
		Path:        configPath,
		Email:       cursorTopLevelString(authInfo, "email"),
		DisplayName: cursorTopLevelString(authInfo, "displayName"),
	}
	if userID, ok := authInfo["userId"].(float64); ok {
		info.UserID = int(userID)
	}
	if info.Email == "" && info.DisplayName == "" && info.UserID == 0 {
		return nil, nil
	}
	return info, nil
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
