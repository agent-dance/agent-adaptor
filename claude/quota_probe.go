package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
)

var claudeQuotaUsageURL = "https://api.anthropic.com/api/oauth/usage"
var claudeQuotaHTTPClient = &http.Client{Timeout: 8 * time.Second}

type claudeQuotaResponse struct {
	FiveHour       *claudeQuotaWindow `json:"five_hour"`
	SevenDay       *claudeQuotaWindow `json:"seven_day"`
	SevenDaySonnet *claudeQuotaWindow `json:"seven_day_sonnet"`
	SevenDayOpus   *claudeQuotaWindow `json:"seven_day_opus"`
	ExtraUsage     *struct {
		IsEnabled   *bool    `json:"is_enabled"`
		Monthly     *float64 `json:"monthly_limit"`
		Used        *float64 `json:"used_credits"`
		Utilization *float64 `json:"utilization"`
		Currency    string   `json:"currency"`
	} `json:"extra_usage"`
}

type claudeQuotaWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

func fetchClaudeQuota(ctx context.Context, token string) ([]agentadaptor.QuotaWindow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeQuotaUsageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := claudeQuotaHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic usage api returned %d", resp.StatusCode)
	}
	var payload claudeQuotaResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	windows := make([]agentadaptor.QuotaWindow, 0, 5)
	if payload.FiveHour != nil {
		windows = append(windows, agentadaptor.QuotaWindow{
			Label:       "Current session",
			UsedPercent: normalizeClaudePercent(payload.FiveHour.Utilization),
			ResetsAt:    payload.FiveHour.ResetsAt,
		})
	}
	if payload.SevenDay != nil {
		windows = append(windows, agentadaptor.QuotaWindow{
			Label:       "Current week (all models)",
			UsedPercent: normalizeClaudePercent(payload.SevenDay.Utilization),
			ResetsAt:    payload.SevenDay.ResetsAt,
		})
	}
	if payload.SevenDaySonnet != nil {
		windows = append(windows, agentadaptor.QuotaWindow{
			Label:       "Current week (Sonnet only)",
			UsedPercent: normalizeClaudePercent(payload.SevenDaySonnet.Utilization),
			ResetsAt:    payload.SevenDaySonnet.ResetsAt,
		})
	}
	if payload.SevenDayOpus != nil {
		windows = append(windows, agentadaptor.QuotaWindow{
			Label:       "Current week (Opus only)",
			UsedPercent: normalizeClaudePercent(payload.SevenDayOpus.Utilization),
			ResetsAt:    payload.SevenDayOpus.ResetsAt,
		})
	}
	if payload.ExtraUsage != nil {
		window := agentadaptor.QuotaWindow{
			Label:  "Extra usage",
			Detail: "Monthly extra usage pool",
		}
		if payload.ExtraUsage.IsEnabled != nil && !*payload.ExtraUsage.IsEnabled {
			window.ValueLabel = "Not enabled"
			window.Detail = "Extra usage not enabled"
		} else {
			window.UsedPercent = normalizeClaudePercent(payload.ExtraUsage.Utilization)
			if payload.ExtraUsage.Monthly != nil && payload.ExtraUsage.Used != nil {
				window.ValueLabel = formatClaudeMoney(*payload.ExtraUsage.Used, payload.ExtraUsage.Currency) + " / " + formatClaudeMoney(*payload.ExtraUsage.Monthly, payload.ExtraUsage.Currency)
			}
		}
		windows = append(windows, window)
	}
	return windows, nil
}

func claudeQuotaReport(ctx context.Context, bindings []agentadaptor.EnvBinding) (agentadaptor.QuotaReport, error) {
	report := agentadaptor.QuotaReport{
		DriverType: DriverType,
		Provider:   "anthropic",
		Source:     "anthropic_oauth_usage",
		Available:  false,
	}
	if enabled, _ := adapterutil.ResolvedTruthyEnv(bindings, "CLAUDE_CODE_USE_BEDROCK"); enabled {
		report.Error = "AWS Bedrock mode does not expose local Claude subscription quota windows"
		return report, nil
	}
	if _, source := adapterutil.ResolvedEnvValue(bindings, "ANTHROPIC_BEDROCK_BASE_URL"); source != "" {
		report.Error = "Anthropic Bedrock base URL mode does not expose local Claude subscription quota windows"
		return report, nil
	}

	credentials, err := readClaudeCredentialInfo(bindings)
	if err != nil {
		report.Error = err.Error()
		return report, nil
	}
	if credentials == nil || credentials.AccessToken == "" {
		if _, source := adapterutil.ResolvedEnvValue(bindings, "ANTHROPIC_API_KEY"); source != "" {
			report.Error = "ANTHROPIC_API_KEY mode does not expose local Claude subscription quota windows"
			return report, nil
		}
		report.Error = "no local Claude OAuth access token found in credentials files"
		return report, nil
	}
	windows, err := fetchClaudeQuota(ctx, credentials.AccessToken)
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

func normalizeClaudePercent(raw *float64) *int {
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

func formatClaudeMoney(raw float64, currency string) string {
	code := currency
	if code == "" {
		code = "USD"
	}
	return fmt.Sprintf("%s %.2f", code, raw/100)
}
