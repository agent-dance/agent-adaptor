package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type auditFixtureDriver struct{}

func (auditFixtureDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "audit-fixture", Runtime: driver.RuntimeCapability{ReportsServices: true}}
}
func (auditFixtureDriver) ValidateConfig(any) error { return nil }
func (auditFixtureDriver) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return driver.Response{
		Output: `{"answer":42}`, Summary: "partial answer", Model: "model", Provider: "provider",
		Metadata: map[string]string{"tag": "observed"}, Usage: &driver.Usage{InputTokens: 7, OutputTokens: 2},
		RawStreams: &driver.RawStreams{Stdout: "complete stdout\n", Stderr: "complete stderr\n",
			Terminal: &driver.TerminalPayload{Event: "result", JSON: json.RawMessage(`{"status":"failed"}`)}},
		Transcript:      []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "actual assistant item"}},
		RuntimeServices: []driver.RuntimeServiceReport{{ID: "observed-service", Status: driver.RuntimeServiceRunning}},
		Failure:         &driver.RunFailure{Code: driver.FailureAgentError, Message: "provider failure"},
	}, nil
}

func failedAuditResult(t *testing.T) (*adaptor.Result, error) {
	t.Helper()
	agent := adaptor.New(auditFixtureDriver{})
	t.Cleanup(func() {
		if err := agent.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	r, err := agent.Send(context.Background(), "original host input", adaptor.MessageID("audit-message")).Result()
	var failure *adaptor.RunError
	if r != nil || !errors.As(err, &failure) || failure.Result == nil {
		t.Fatalf("fixture did not return a partial RunError: result=%v err=%v", r, err)
	}
	failure.Result = failure.Result.WithSubagents([]adaptor.SubagentResult{{Member: "worker", Task: "task", State: "failed"}})
	return failure.Result, fmt.Errorf("wrapped failure: %w", err)
}

func TestResultAuditRetainsFailureAndEveryAuditSurface(t *testing.T) {
	r, err := failedAuditResult(t)
	audit := resultAudit(nil, err)
	for _, key := range []string{"text", "summary", "runID", "model", "provider", "metadata", "turns", "usage", "raw", "transcript", "services", "messages", "subagents", "error", "failure", "decoded"} {
		if _, ok := audit[key]; !ok {
			t.Errorf("missing audit field %q", key)
		}
	}
	if audit["error"] != err.Error() || audit["text"] != r.Text || audit["runID"] != r.RunID {
		t.Fatalf("failure or result identity was lost: %v", audit)
	}
	if got := audit["raw"].(adaptor.RawStreams); !reflect.DeepEqual(got, r.Raw()) || got.Terminal == nil {
		t.Fatalf("raw streams or official terminal lost: %+v", got)
	}
	if got := audit["transcript"].([]adaptor.TranscriptItem); len(got) == 0 || !reflect.DeepEqual(got, r.Transcript()) {
		t.Fatalf("transcript lost: %+v", got)
	}
	if got := audit["messages"].([]adaptor.MessageOutcome); len(got) != 1 || got[0].ID != "audit-message" || got[0].Text != "original host input" {
		t.Fatalf("message outcome lost: %+v", got)
	}
	if len(audit["services"].([]adaptor.ServiceReport)) != 1 || len(audit["subagents"].([]adaptor.SubagentResult)) != 1 {
		t.Fatal("services or subagent results lost")
	}
	if audit["decoded"].(map[string]any)["answer"] != float64(42) {
		t.Fatalf("decoded output=%v", audit["decoded"])
	}
}

func TestResultAuditKeepsDecodeAndInfrastructureFailures(t *testing.T) {
	err := errors.New("transport disconnected")
	audit := resultAudit(&adaptor.Result{Text: "incomplete JSON"}, err)
	if audit["error"] != err.Error() || audit["decodeError"] == nil {
		t.Fatalf("failure lost: %v", audit)
	}
	if _, ok := audit["decoded"]; ok {
		t.Fatal("invalid output was presented as decoded")
	}
	if got := resultAudit(nil, err); got["error"] != err.Error() {
		t.Fatalf("nil-result error lost: %v", got)
	}
	if got := resultAudit(&adaptor.Result{Text: `{"explicit":true}`}, &adaptor.RunError{Result: &adaptor.Result{Text: "other"}}); got["text"] != `{"explicit":true}` {
		t.Fatal("explicit result was overwritten by error result")
	}
}

func TestWriteJSONRedactsAuditAndEscapedLiterals(t *testing.T) {
	r, err := failedAuditResult(t)
	secrets := []string{"complete stdout", "actual assistant item", "original host input", "token\"with\\escape\n<end>", ""}
	quoted, _ := json.Marshal(secrets[3])
	value := resultAudit(r, err)
	value["sensitive"] = secrets[3]
	value["alreadyEscaped"] = string(quoted[1 : len(quoted)-1])
	path := filepath.Join(t.TempDir(), "private", "nested", "audit.json")
	if err := writeJSON(path, value, secrets); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("redaction damaged JSON: %v", err)
	}
	if saved["sensitive"] != "[REDACTED]" || saved["alreadyEscaped"] != "[REDACTED]" {
		t.Fatal("quoted, escaped or HTML-sensitive secret survived")
	}
	if raw := saved["raw"].(map[string]any); raw["Stdout"] != "[REDACTED]\n" || raw["Stderr"] != "complete stderr\n" || raw["Terminal"] == nil {
		t.Fatalf("raw audit content changed unexpectedly: %v", raw)
	}
	if got := saved["transcript"].([]any)[0].(map[string]any)["Text"]; got != "[REDACTED]" {
		t.Fatalf("transcript secret survived: %v", got)
	}
	if got := saved["messages"].([]any)[0].(map[string]any)["Text"]; got != "[REDACTED]" {
		t.Fatalf("message secret survived: %v", got)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		dir, _ := os.Stat(filepath.Dir(path))
		if info.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
			t.Fatalf("audit permissions file=%o dir=%o", info.Mode().Perm(), dir.Mode().Perm())
		}
	}
}

func TestWriteJSONPreservesExistingFileOnEncodingFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.json")
	if err := os.WriteFile(path, []byte("previous audit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(path, make(chan int), nil); err == nil || !strings.Contains(err.Error(), "encode audit JSON") {
		t.Fatalf("unsupported value error=%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "previous audit" {
		t.Fatalf("failed write destroyed previous audit: %q, %v", data, err)
	}
}

func TestWriteJSONRestrictsAndReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("old content", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(path, map[string]string{"value": "private-token"}, []string{"private-token"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) || strings.Contains(string(data), "old content") || strings.Contains(string(data), "private-token") {
		t.Fatal("replacement report retained old content or a secret")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("existing report permissions=%v err=%v", info, err)
		}
	}
}
