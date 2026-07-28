package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
)

var codexQuotaUsageURL = "https://chatgpt.com/backend-api/wham/usage"
var codexQuotaHTTPClient = &http.Client{Timeout: 8 * time.Second}

type codexQuotaResponse struct {
	RateLimit *struct {
		PrimaryWindow   *codexQuotaWindow `json:"primary_window"`
		SecondaryWindow *codexQuotaWindow `json:"secondary_window"`
	} `json:"rate_limit"`
	Credits *struct {
		Balance   *float64 `json:"balance"`
		Unlimited bool     `json:"unlimited"`
	} `json:"credits"`
}

type codexQuotaWindow struct {
	UsedPercent *float64 `json:"used_percent"`
	ResetAt     any      `json:"reset_at"`
}

func fetchCodexQuota(ctx context.Context, auth *codexAuthInfo) ([]driver.QuotaWindow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexQuotaUsageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	if auth.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", auth.AccountID)
	}
	resp, err := codexQuotaHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("chatgpt wham api returned %d", resp.StatusCode)
	}
	var payload codexQuotaResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	windows := make([]driver.QuotaWindow, 0, 3)
	if payload.RateLimit != nil && payload.RateLimit.PrimaryWindow != nil {
		windows = append(windows, driver.QuotaWindow{
			Label:       "5h limit",
			UsedPercent: normalizeCodexUsedPercent(payload.RateLimit.PrimaryWindow.UsedPercent),
			ResetsAt:    quotaResetString(payload.RateLimit.PrimaryWindow.ResetAt),
		})
	}
	if payload.RateLimit != nil && payload.RateLimit.SecondaryWindow != nil {
		windows = append(windows, driver.QuotaWindow{
			Label:       "Weekly limit",
			UsedPercent: normalizeCodexUsedPercent(payload.RateLimit.SecondaryWindow.UsedPercent),
			ResetsAt:    quotaResetString(payload.RateLimit.SecondaryWindow.ResetAt),
		})
	}
	if payload.Credits != nil && !payload.Credits.Unlimited && payload.Credits.Balance != nil {
		windows = append(windows, driver.QuotaWindow{
			Label:      "Credits",
			ValueLabel: fmt.Sprintf("$%.2f remaining", *payload.Credits.Balance/100),
		})
	}
	return windows, nil
}

func codexQuotaReport(ctx context.Context, bindings []driver.EnvBinding) (driver.QuotaReport, error) {
	report := driver.QuotaReport{
		DriverType: DriverType,
		Provider:   "openai",
		Source:     "codex_wham",
		Available:  false,
	}
	auth, err := readCodexAuthInfo(bindings)
	if err != nil {
		report.Error = err.Error()
		return report, nil
	}
	if auth == nil || auth.AccessToken == "" {
		if _, source := driverutil.ResolvedEnvValue(bindings, "OPENAI_API_KEY"); source != "" {
			report.Error = "OPENAI_API_KEY mode does not expose local Codex subscription quota windows"
			return report, nil
		}
		report.Error = "no local Codex auth token found in auth.json"
		return report, nil
	}
	windows, err := fetchCodexQuota(ctx, auth)
	if err != nil {
		report.Error = err.Error()
		return report, nil
	}
	report.Available = len(windows) > 0
	report.Windows = windows
	if !report.Available {
		report.Error = "quota probe returned no quota windows"
	}
	return report, nil
}

func normalizeCodexUsedPercent(raw *float64) *int {
	if raw == nil {
		return nil
	}
	value := *raw
	if value < 1 {
		value *= 100
	}
	normalized := int(value + 0.5)
	if normalized > 100 {
		normalized = 100
	}
	if normalized < 0 {
		normalized = 0
	}
	return &normalized
}

func quotaResetString(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case float64:
		return time.Unix(int64(value), 0).UTC().Format(time.RFC3339)
	default:
		return ""
	}
}
