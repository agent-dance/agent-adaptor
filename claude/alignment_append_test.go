package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
	"github.com/agent-dance/agent-adaptor/memory"
)

func alignmentAppendFixture(t *testing.T, resident bool) *alignmentClaudeFixture {
	t.Helper()
	f := newAlignmentClaudeFixture(t, resident)
	f.cfg.Env = append(f.cfg.Env, driver.EnvBinding{Name: "APPEND_RECORD", Value: filepath.Join(f.root, "APPEND_RECORD")})
	return f
}
func alignmentAppendRecords(t *testing.T, f *alignmentClaudeFixture) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, line := range f.lines(t, "APPEND_RECORD") {
		var record map[string]string
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		out = append(out, record)
	}
	return out
}
func TestAlignmentClaudeAppendNativeAndClearing(t *testing.T) {
	text := " 宿主\n\"native\"\\\t "
	f := alignmentAppendFixture(t, false)
	a := f.agent(adaptor.WithAppendSystemPrompt(text), adaptor.WithPolicy(alignmentClaudePolicy()))
	defer a.Close(context.Background())
	for _, tc := range []struct {
		value    string
		override bool
	}{{text, false}, {"call\n覆盖", true}, {"", true}, {" \n", true}, {strings.Repeat("长", 12000), true}} {
		opts := []adaptor.CallOption{}
		if tc.override {
			opts = append(opts, adaptor.WithAppendSystemPrompt(tc.value))
		}
		result, err := a.Run(context.Background(), "native input", opts...)
		if err != nil {
			t.Fatal(err)
		}
		records := alignmentAppendRecords(t, f)
		last := records[len(records)-1]
		if last["text"] != tc.value {
			t.Fatalf("native text mismatch %d/%d", len(last["text"]), len(tc.value))
		}
		if tc.value == "" && last["path"] != "" {
			t.Fatal("empty append allocated file")
		}
		if last["path"] != "" {
			if _, err := os.Stat(last["path"]); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("oneshot carrier not reclaimed")
			}
		}
		if strings.Contains(result.Text, text) || strings.Contains(result.Raw().Stdout, text) {
			t.Fatal("append merged into output")
		}
	}
}

func TestAlignmentClaudeAppendPersistentHandoff(t *testing.T) {
	f := alignmentAppendFixture(t, true)
	a := f.agent(adaptor.WithThreadStore(memory.NewStore()), adaptor.WithAppendSystemPrompt("original"), adaptor.WithPolicy(alignmentClaudePolicy()))
	defer a.Close(context.Background())
	th := a.Thread("append-thread")
	run := func(opts ...adaptor.CallOption) {
		t.Helper()
		if _, err := th.Run(context.Background(), "native input", opts...); err != nil {
			t.Fatal(err)
		}
	}
	run()
	run()
	records := alignmentAppendRecords(t, f)
	if len(records) != 1 {
		t.Fatalf("reuse spawns=%d", len(records))
	}
	if raw, err := os.ReadFile(records[0]["path"]); err != nil || string(raw) != "original" {
		t.Fatal("resident lost carrier after run")
	}
	if _, err := a.Thread("append-thread", adaptor.ResumeOnly()).Run(context.Background(), "native input", adaptor.WithAppendSystemPrompt("changed")); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("ResumeOnly drift=%v", err)
	}
	if len(alignmentAppendRecords(t, f)) != 1 {
		t.Fatal("incompatible resume started replacement")
	}
	run(adaptor.WithAppendSystemPrompt("changed"))
	records = alignmentAppendRecords(t, f)
	if len(records) != 2 || records[1]["text"] != "changed" {
		t.Fatalf("changed carrier=%+v", records)
	}
	if _, err := os.Stat(records[0]["path"]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old carrier survives successful handoff")
	}
	run(adaptor.WithAppendSystemPrompt("changed"), adaptor.WithSpawn())
	records = alignmentAppendRecords(t, f)
	if len(records) != 3 {
		t.Fatal("WithSpawn registered or omitted process")
	}
	for _, r := range records {
		if _, err := os.Stat(r["path"]); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("WithSpawn left live carrier")
		}
	}
	run(adaptor.WithAppendSystemPrompt("changed"))
	run(adaptor.WithAppendSystemPrompt(""))
	if len(f.lines(t, "OVERLAP_FILE")) != 0 {
		t.Fatal("overlapping writer")
	}
	records = alignmentAppendRecords(t, f)
	if len(records) != 5 || records[4]["path"] != "" {
		t.Fatalf("clear handoff=%+v", records)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r["path"] != "" {
			if _, err := os.Stat(r["path"]); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("Close did not reclaim carrier")
			}
		}
	}
}

func TestAlignmentClaudeAppendCodecAndConflict(t *testing.T) {
	for _, flag := range []string{"--system-prompt", "--system-prompt-file", "--append-system-prompt", "--append-system-prompt-file"} {
		for _, args := range [][]string{{flag, "secret"}, {flag + "=secret"}} {
			for _, text := range []string{"", "append"} {
				d := Driver(Config{CommonConfig: CommonConfig{Command: "must-not-run", ExtraArgs: args}})
				_, err := d.Run(context.Background(), driver.Request{AppendSystemPrompt: text}, &streamSink{})
				var typed *driver.SystemPromptUnsupportedError
				if !errors.As(err, &typed) || typed.Reason != "conflicting_extra_args" || strings.Contains(err.Error(), "secret") {
					t.Fatalf("extra args=%v", err)
				}
			}
		}
	}
	state := &driver.SessionState{ResumeID: "s", Data: map[string]string{appendSystemPromptFingerprintKey: systemprompt.Fingerprint("native")}}
	codec := sessionCodec{}
	if !reflect.DeepEqual(codec.FromParams(codec.ToParams(state)), &driver.SessionState{ResumeID: "s", DisplayID: "s", Data: state.Data}) {
		t.Fatal("codec dropped append hash")
	}
	req := driver.Request{AppendSystemPrompt: "native", Session: &driver.SessionContext{State: state}}
	if err := validateClaudeSessionGuard(req, "", ""); err != nil {
		t.Fatal(err)
	}
	req.AppendSystemPrompt = ""
	if err := validateClaudeSessionGuard(req, "", ""); err == nil {
		t.Fatal("direct SPI clear bypassed guard")
	}
	spec := persistentSpec{command: "claude", appendSystemPrompt: "native", resumeID: "old"}
	first := spec.sig()
	spec.resumeID = "new"
	if spec.sig() != first {
		t.Fatal("resumeID changed startup signature")
	}
	spec.appendSystemPrompt = "change"
	if spec.sig() == first {
		t.Fatal("append omitted from startup signature")
	}
}

func TestAlignmentClaudeAppendPrewarmAndCleanupRetry(t *testing.T) {
	f := alignmentAppendFixture(t, true)
	a := f.agent(adaptor.WithThreadStore(memory.NewStore()), adaptor.WithAppendSystemPrompt("prewarm"), adaptor.WithPolicy(alignmentClaudePolicy()))
	defer a.Close(context.Background())
	th := a.Thread("prewarm")
	schema := adaptor.WithSchemaJSON([]byte(alignmentClaudeSchema))
	if _, err := th.Run(context.Background(), "native input", schema); err != nil {
		t.Fatal(err)
	}
	// Prewarm exists at the pool boundary even if the child has not run yet.
	f.pool.mu.Lock()
	var resident *liveProcess
	for lp := range f.pool.all {
		resident = lp
	}
	f.pool.mu.Unlock()
	if resident == nil || resident.appendFile == nil {
		t.Fatal("native schema prewarm lost carrier")
	}
	if err := resident.appendFile.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := th.Run(context.Background(), "native input"); err != nil {
		t.Fatal(err)
	}
	// Directory contamination makes owned Close observably retryable after Wait.
	extra := filepath.Join(filepath.Dir(resident.appendFile.Path()), "do-not-delete")
	if err := os.WriteFile(extra, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err == nil {
		t.Fatal("cleanup failure swallowed")
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatal("deleted unrelated object")
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAlignmentClaudeAppendPreparationSafety(t *testing.T) {
	args, file, err := prepareClaudeAppend(context.Background(), "claude", []string{"--print"}, "abcd")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if len(args) != 3 || args[1] != "--append-system-prompt-file" {
		t.Fatalf("argv=%v", args)
	}
	if err := os.WriteFile(file.Path(), []byte("dcba"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := file.Verify(context.Background()); err == nil {
		t.Fatal("same-length tampering passed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := prepareClaudeAppend(ctx, "claude", nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestAlignmentClaudePublicObservationAndOutput(t *testing.T) {
	f := alignmentAppendFixture(t, false)
	frames := alignmentTool("mcp-call", "mcp__known__op", "", map[string]any{"input": "secret"}) + alignmentToolResult("mcp-call", "", false, nil) + alignmentTool("clear", "TodoWrite", "", map[string]any{"todos": []any{}}) + alignmentToolResult("clear", "", false, nil)
	f.cfg.Env = append(f.cfg.Env, driver.EnvBinding{Name: "ADOPTION_FRAMES", Value: frames})
	// Catalog is populated by the final Request; use a configured wrapper only
	// to inject fixture catalog data, with no additional execution or event path.
	d := alignmentCatalogDriver{configuredDriver{adapter: adapter{persistent: f.pool}, cfg: f.cfg}}
	observed := []adaptor.Event{}
	service := alignmentObservationService{events: &observed}
	a := adaptor.New(d, adaptor.WithPolicy(alignmentClaudePolicy()), adaptor.WithAppendSystemPrompt("APPEND-NONCE"), adaptor.WithRunServices(service))
	defer a.Close(context.Background())
	one, err := a.Run(context.Background(), "native input")
	if err != nil {
		t.Fatal(err)
	}
	st := a.Stream(context.Background(), "native input")
	var publicFacts int
	for e := range st.Events() {
		data, _ := json.Marshal(e)
		if strings.Contains(string(data), "APPEND-NONCE") {
			t.Fatal("SDK diagnostics leaked append text")
		}
		switch e.(type) {
		case adaptor.CapabilityInvocation, adaptor.TodoUpdated:
			publicFacts++
		}
	}
	two, err := st.Result()
	if err != nil {
		t.Fatal(err)
	}
	assertAlignmentClaudeEqual(t, one, two)
	if publicFacts != 3 || len(observed) != 6 {
		t.Fatalf("public/observer=%d/%d", publicFacts, len(observed))
	}
	if one.Raw().Terminal == nil || !strings.Contains(one.Raw().Stdout, "tool output") || len(one.Transcript()) < 5 || one.Usage == nil {
		t.Fatal("output contract lost")
	}
	_, failure := a.Run(context.Background(), "native input nonzero")
	var runErr *adaptor.RunError
	if !errors.As(failure, &runErr) || runErr.Result == nil || runErr.Result.Raw().Terminal == nil || !strings.Contains(runErr.Result.Raw().Stdout, "tool output") || len(runErr.Result.Transcript()) < 5 {
		t.Fatalf("new facts lost partial audit: %v", failure)
	}
	for _, e := range observed {
		data, _ := json.Marshal(e)
		if strings.Contains(string(data), "APPEND-NONCE") || strings.Contains(string(data), "secret") {
			t.Fatal("observation metadata leak")
		}
	}
}

type alignmentCatalogDriver struct{ configuredDriver }

func (d alignmentCatalogDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	req.MCP = driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "known", Transport: driver.MCPTransportStdio, Command: "unused-fixture"}}}
	return d.configuredDriver.Run(ctx, req, sink)
}

type alignmentObservationService struct{ events *[]adaptor.Event }

func (s alignmentObservationService) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	return adaptor.RunAttachment{Observation: adaptor.ObservationDemand{CapabilityInvocations: true, Todos: true}, Observer: func(_ context.Context, _ adaptor.RunEventInfo, e adaptor.Event) error {
		*s.events = append(*s.events, e)
		return nil
	}}, nil
}
func (alignmentObservationService) DetachRun(context.Context, string) error { return nil }
