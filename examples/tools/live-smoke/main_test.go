package main

import (
	"errors"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestClassifyRun(t *testing.T) {
	tests := []struct {
		name   string
		result agentadaptor.RunResult
		err    error
		want   smokeStatus
	}{
		{name: "passed", result: agentadaptor.RunResult{Output: sentinel}, want: statusPassed},
		{name: "missing sentinel", result: agentadaptor.RunResult{Output: "different"}, want: statusRunFailed},
		{name: "sdk error", err: errors.New("protocol closed"), want: statusRunFailed},
		{name: "auth error", err: errors.New("authentication required"), want: statusEnvironmentFailed},
		{name: "business failure", result: agentadaptor.RunResult{Failure: &agentadaptor.RunFailure{Message: "policy rejected"}}, want: statusRunFailed},
		{name: "quota failure", result: agentadaptor.RunResult{Failure: &agentadaptor.RunFailure{Message: "insufficient quota"}}, want: statusEnvironmentFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := classifyRun(tt.result, tt.err)
			if got != tt.want {
				t.Fatalf("classifyRun() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFinishExitCodes(t *testing.T) {
	for status, want := range map[smokeStatus]int{
		statusPassed: 0, statusSkipped: 0, statusEnvironmentFailed: 2, statusRunFailed: 3,
	} {
		if got := finish(report{Status: status}); got != want {
			t.Errorf("finish(%q) = %d, want %d", status, got, want)
		}
	}
}

func TestHasCredentialWarning(t *testing.T) {
	missing := agentadaptor.EnvironmentReport{Checks: []agentadaptor.EnvironmentCheck{{Code: "claude_credentials_missing"}}}
	if !hasCredentialWarning(missing) {
		t.Fatal("expected credentials warning to be detected")
	}
	configured := agentadaptor.EnvironmentReport{Checks: []agentadaptor.EnvironmentCheck{{Code: "claude_credentials_present"}}}
	if hasCredentialWarning(configured) {
		t.Fatal("did not expect configured credentials to be treated as missing")
	}
}
