package codex

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/configprobe"
)

type codexAuthInfo struct {
	AccessToken string
	Path        string
	AccountID   string
	Email       string
	PlanType    string
	LastRefresh string
}

func codexAuthChecks(bindings []agentadaptor.EnvBinding) []agentadaptor.EnvironmentCheck {
	if _, source := adapterutil.ResolvedEnvValue(bindings, "OPENAI_API_KEY"); source != "" {
		return []agentadaptor.EnvironmentCheck{{
			Code:    "openai_api_key_present",
			Level:   "info",
			Message: "OPENAI_API_KEY is available for Codex authentication.",
			Detail:  authSourceDetail(source),
		}}
	}

	auth, err := readCodexAuthInfo(bindings)
	if err != nil {
		return []agentadaptor.EnvironmentCheck{{
			Code:    "codex_auth_unreadable",
			Level:   "warn",
			Message: "Codex auth.json exists but could not be parsed.",
			Detail:  err.Error(),
			Hint:    "Re-run `codex login` or remove the broken auth.json and authenticate again.",
		}}
	}
	if auth == nil {
		return []agentadaptor.EnvironmentCheck{{
			Code:    "codex_auth_missing",
			Level:   "warn",
			Message: "No Codex auth.json or OPENAI_API_KEY was found.",
			Hint:    "Run `codex login`, set OPENAI_API_KEY in CommonConfig.Env, or point AgentProfileDir / CODEX_HOME at an existing Codex profile.",
		}}
	}

	checks := []agentadaptor.EnvironmentCheck{{
		Code:    "codex_auth_present",
		Level:   "info",
		Message: "Codex native auth.json is present.",
		Detail:  auth.Path,
	}}
	if auth.Email != "" {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "codex_auth_email",
			Level:   "info",
			Message: "Codex auth identifies the logged-in account.",
			Detail:  auth.Email,
		})
	}
	if auth.PlanType != "" {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "codex_auth_plan",
			Level:   "info",
			Message: "Codex auth token exposes the ChatGPT plan type.",
			Detail:  auth.PlanType,
		})
	}
	if auth.AccountID != "" {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "codex_auth_account",
			Level:   "info",
			Message: "Codex auth token includes an account id.",
			Detail:  auth.AccountID,
		})
	}
	if auth.LastRefresh != "" {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "codex_auth_last_refresh",
			Level:   "info",
			Message: "Codex auth metadata reports the last refresh timestamp.",
			Detail:  auth.LastRefresh,
		})
	}
	return checks
}

func readCodexAuthInfo(bindings []agentadaptor.EnvBinding) (*codexAuthInfo, error) {
	authPath := filepath.Join(resolveSharedCodexHome(bindings), "auth.json")
	payload, err := configprobe.ReadJSONObject(authPath)
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	accessToken := nestedString(payload, "tokens", "access_token")
	if accessToken == "" {
		accessToken = topLevelString(payload, "accessToken")
	}
	if accessToken == "" {
		return nil, nil
	}

	idToken := nestedString(payload, "tokens", "id_token")
	email, planType := parseCodexTokenHints(idToken, accessToken)

	return &codexAuthInfo{
		AccessToken: accessToken,
		Path:        authPath,
		AccountID:   firstNonEmpty(nestedString(payload, "tokens", "account_id"), topLevelString(payload, "accountId")),
		Email:       email,
		PlanType:    planType,
		LastRefresh: topLevelString(payload, "last_refresh"),
	}, nil
}

func parseCodexTokenHints(tokens ...string) (string, string) {
	for _, token := range tokens {
		payload := decodeJWTPayload(token)
		if payload == nil {
			continue
		}
		email := firstNonEmpty(
			topLevelString(payload, "email"),
			nestedString(payload, "https://api.openai.com/profile", "email"),
			nestedString(payload, "https://api.openai.com/auth", "chatgpt_user_email"),
		)
		planType := nestedString(payload, "https://api.openai.com/auth", "chatgpt_plan_type")
		if email != "" || planType != "" {
			return email, planType
		}
	}
	return "", ""
}

func decodeJWTPayload(token string) map[string]any {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return payload
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
