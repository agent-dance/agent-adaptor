package appserver

import (
	"encoding/json"
	"errors"
)

// The generated value DTO cannot distinguish absent/null counters from zero.
// Validate the required fields from ThreadTokenUsageUpdatedNotification.json
// before using it, including historical replay eligible for the narrow fence.
type requiredTokenCounters struct {
	CachedInputTokens     *int `json:"cachedInputTokens"`
	InputTokens           *int `json:"inputTokens"`
	OutputTokens          *int `json:"outputTokens"`
	ReasoningOutputTokens *int `json:"reasoningOutputTokens"`
	TotalTokens           *int `json:"totalTokens"`
}

func decodeThreadTokenUsage(params json.RawMessage) (ThreadTokenUsageUpdatedNotification, error) {
	var body ThreadTokenUsageUpdatedNotification
	var required struct {
		ThreadID   *string `json:"threadId"`
		TurnID     *string `json:"turnId"`
		TokenUsage *struct {
			Last  *requiredTokenCounters `json:"last"`
			Total *requiredTokenCounters `json:"total"`
		} `json:"tokenUsage"`
	}
	invalid := errors.New("token usage requires complete non-negative integer counters")
	if err := json.Unmarshal(params, &required); err != nil {
		return body, invalid
	}
	if required.ThreadID == nil || required.TurnID == nil || required.TokenUsage == nil {
		return body, invalid
	}
	for _, counters := range []*requiredTokenCounters{required.TokenUsage.Last, required.TokenUsage.Total} {
		if counters == nil {
			return body, invalid
		}
		for _, value := range []*int{counters.CachedInputTokens, counters.InputTokens, counters.OutputTokens, counters.ReasoningOutputTokens, counters.TotalTokens} {
			if value == nil || *value < 0 {
				return body, invalid
			}
		}
	}
	if err := json.Unmarshal(params, &body); err != nil {
		return body, invalid
	}
	return body, nil
}
