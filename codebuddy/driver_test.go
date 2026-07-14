package codebuddy

import (
	"strings"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func timeNow() time.Time { return time.Now().UTC() }

func argIndex(args []string, name string) int {
	for i, a := range args {
		if a == name {
			return i
		}
	}
	return -1
}

func hasArg(args []string, name string) bool { return argIndex(args, name) >= 0 }

func TestDescriptorCapabilities(t *testing.T) {
	d := adapter{}.Descriptor()
	if d.Type != DriverType {
		t.Fatalf("type = %q, want %q", d.Type, DriverType)
	}
	if !d.Sessions.SupportsResume {
		t.Errorf("expected SupportsResume=true")
	}
	if !d.Skills.Supported || d.Skills.Mode != agentadaptor.SkillSyncPersistent {
		t.Errorf("expected SkillSyncPersistent, got %+v", d.Skills)
	}
	if !d.MCP.Supported || !d.MCP.Stdio || !d.MCP.HTTP {
		t.Errorf("expected MCP stdio+http, got %+v", d.MCP)
	}
	caps := d.RunPolicyCaps
	if caps.WebSearch || caps.Browser || caps.Isolation {
		t.Errorf("expected WebSearch/Browser/Isolation=false, got %+v", caps)
	}
	if !caps.Permission.Ask || !caps.Permission.AutoApprove || !caps.Permission.AutoReject {
		t.Errorf("permission caps = %+v, want Ask+AutoApprove+AutoReject", caps.Permission)
	}
	if !caps.PlanReview.Ask || !caps.PlanReview.AutoApprove || !caps.PlanReview.AutoReject {
		t.Errorf("plan caps = %+v", caps.PlanReview)
	}
	if caps.Question.Ask || caps.Question.AutoReject || caps.Question.Retry {
		t.Errorf("question caps = %+v, want unsupported", caps.Question)
	}
}

func TestValidateConfig(t *testing.T) {
	a := adapter{}
	if err := a.ValidateConfig(agentadaptor.CodeBuddyConfig{}); err != nil {
		t.Errorf("empty config should validate, got %v", err)
	}
	if err := a.ValidateConfig(agentadaptor.CodeBuddyConfig{PermissionMode: agentadaptor.CodeBuddyPermissionAcceptEdits}); err != nil {
		t.Errorf("acceptEdits should validate, got %v", err)
	}
	if err := a.ValidateConfig(agentadaptor.CodeBuddyConfig{PermissionMode: "bogus"}); err == nil {
		t.Errorf("bogus permission mode should fail")
	}
	if err := a.ValidateConfig(agentadaptor.CodeBuddyConfig{Effort: "medium"}); err != nil {
		t.Errorf("medium effort should validate, got %v", err)
	}
	if err := a.ValidateConfig(agentadaptor.CodeBuddyConfig{Effort: "ludicrous"}); err == nil {
		t.Errorf("bad effort should fail")
	}
	if err := a.ValidateConfig(struct{}{}); err == nil {
		t.Errorf("wrong type should fail")
	}
}

func TestBuildExecArgsHeadless(t *testing.T) {
	cfg := agentadaptor.CodeBuddyConfig{Model: "claude-sonnet-5", Effort: "high", MaxTurnsPerRun: 5}
	req := agentadaptor.DriverRunRequest{Streaming: true}
	args := buildExecArgs(cfg, req, agentadaptor.CodeBuddyPermissionAcceptEdits)

	if !hasArg(args, "--print") {
		t.Errorf("missing --print: %v", args)
	}
	if i := argIndex(args, "--output-format"); i < 0 || args[i+1] != "stream-json" {
		t.Errorf("missing --output-format stream-json: %v", args)
	}
	if !hasArg(args, "--include-partial-messages") {
		t.Errorf("streaming should add --include-partial-messages: %v", args)
	}
	if i := argIndex(args, "--model"); i < 0 || args[i+1] != "claude-sonnet-5" {
		t.Errorf("missing --model: %v", args)
	}
	if i := argIndex(args, "--effort"); i < 0 || args[i+1] != "high" {
		t.Errorf("missing --effort: %v", args)
	}
	if i := argIndex(args, "--max-turns"); i < 0 || args[i+1] != "5" {
		t.Errorf("missing --max-turns: %v", args)
	}
	if i := argIndex(args, "--permission-mode"); i < 0 || args[i+1] != "acceptEdits" {
		t.Errorf("missing --permission-mode: %v", args)
	}
}

func TestBuildExecArgsResumeAndStructured(t *testing.T) {
	cfg := agentadaptor.CodeBuddyConfig{}
	req := agentadaptor.DriverRunRequest{
		Session: &agentadaptor.DriverSessionContext{State: &agentadaptor.DriverSessionState{ResumeID: "sess-1"}},
		OutputSchema: &agentadaptor.OutputSchema{
			Mode:       agentadaptor.StructuredOutputNativeStrict,
			SchemaJSON: []byte(`{"type":"object"}`),
		},
	}
	args := buildExecArgs(cfg, req, agentadaptor.CodeBuddyPermissionUnset)
	if i := argIndex(args, "--resume"); i < 0 || args[i+1] != "sess-1" {
		t.Errorf("missing --resume: %v", args)
	}
	if i := argIndex(args, "--output-format"); i < 0 || args[i+1] != "json" {
		t.Errorf("structured output should use --output-format json: %v", args)
	}
	if !hasArg(args, "--json-schema") {
		t.Errorf("structured output should add --json-schema: %v", args)
	}
	if hasArg(args, "--permission-mode") {
		t.Errorf("unset permission mode should not emit flag: %v", args)
	}
}

func TestHeadlessPermissionMode(t *testing.T) {
	// explicit override wins
	cfg := agentadaptor.CodeBuddyConfig{PermissionMode: agentadaptor.CodeBuddyPermissionPlan}
	if got := headlessPermissionMode(cfg, agentadaptor.RunPolicy{}); got != agentadaptor.CodeBuddyPermissionPlan {
		t.Errorf("override: got %q", got)
	}
	// AutoApprove -> bypass
	autoApprove := agentadaptor.RunPolicy{HumanDecision: agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAutoApprove}}
	if got := headlessPermissionMode(agentadaptor.CodeBuddyConfig{}, autoApprove); got != agentadaptor.CodeBuddyPermissionBypass {
		t.Errorf("auto approve: got %q, want bypass", got)
	}
}

func TestWantsACP(t *testing.T) {
	cases := []struct {
		name string
		p    agentadaptor.HumanDecisionPolicy
		want bool
	}{
		{"empty", agentadaptor.HumanDecisionPolicy{}, false},
		{"all auto approve", agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAutoApprove, PlanReview: agentadaptor.HumanDecisionAutoApprove}, false},
		{"permission ask", agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk}, true},
		{"plan ask", agentadaptor.HumanDecisionPolicy{PlanReview: agentadaptor.HumanDecisionAsk}, true},
		{"question ask", agentadaptor.HumanDecisionPolicy{Question: agentadaptor.QuestionAsk}, true},
		{"permission auto reject", agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAutoReject}, true},
		{"question auto reject", agentadaptor.HumanDecisionPolicy{Question: agentadaptor.QuestionAutoReject}, true},
	}
	for _, tc := range cases {
		if got := wantsACP(tc.p); got != tc.want {
			t.Errorf("%s: wantsACP = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// realHeadlessOutput is a captured `codebuddy -p --output-format stream-json`
// transcript (trimmed) used to exercise the parser without the live CLI.
const realHeadlessOutput = `{"type":"system","subtype":"init","session_id":"791ecd9d","model":"claude-haiku-4.5","permissionMode":"default"}
{"type":"assistant","session_id":"791ecd9d","message":{"id":"m1","content":[{"type":"text","text":"OK"}],"model":"claude-haiku-4.5","role":"assistant"}}
{"type":"result","subtype":"success","is_error":false,"result":"OK","session_id":"791ecd9d","num_turns":2,"total_cost_usd":0,"usage":{"input_tokens":25445,"output_tokens":4,"cache_read_input_tokens":0}}
`

func TestParserHeadlessStreamJSON(t *testing.T) {
	p := newParser(nil)
	if err := p.onChunk("stdout", []byte(realHeadlessOutput), timeNow()); err != nil {
		t.Fatalf("onChunk: %v", err)
	}
	p.finalize()

	if got := p.buildOutput(); got != "OK" {
		t.Errorf("output = %q, want OK", got)
	}
	if p.finalSummary() != "OK" {
		t.Errorf("summary = %q, want OK", p.finalSummary())
	}
	if p.usage == nil || p.usage.InputTokens != 25445 || p.usage.OutputTokens != 4 {
		t.Errorf("usage = %+v", p.usage)
	}
	cp := p.checkpoint(0)
	if cp == nil || cp.State == nil || cp.State.ResumeID != "791ecd9d" {
		t.Errorf("checkpoint = %+v", cp)
	}
}

func TestACPDecisionKind(t *testing.T) {
	if got := acpDecisionKind(acpToolCall{Title: "AskUserQuestion"}); got != agentadaptor.HumanDecisionQuestion {
		t.Errorf("askuserquestion -> %v", got)
	}
	if got := acpDecisionKind(acpToolCall{Title: "ExitPlanMode"}); got != agentadaptor.HumanDecisionPlanReview {
		t.Errorf("exitplanmode -> %v", got)
	}
	if got := acpDecisionKind(acpToolCall{Title: "Bash", Kind: "execute"}); got != agentadaptor.HumanDecisionPermission {
		t.Errorf("bash -> %v", got)
	}
}

func TestACPOutcome(t *testing.T) {
	options := []acpPermissionOpt{
		{OptionID: "a1", Kind: "allow_once"},
		{OptionID: "a2", Kind: "allow_always"},
		{OptionID: "r1", Kind: "reject_once"},
	}
	approve := acpOutcome(options, agentadaptor.DecisionResponse{Result: agentadaptor.DecisionApproved})
	if outcomeOptionID(approve) != "a1" {
		t.Errorf("approve outcome = %v, want a1", approve)
	}
	reject := acpOutcome(options, agentadaptor.DecisionResponse{Result: agentadaptor.DecisionRejected})
	if outcomeOptionID(reject) != "r1" {
		t.Errorf("reject outcome = %v, want r1", reject)
	}
	none := acpOutcome([]acpPermissionOpt{{OptionID: "x", Kind: "allow_once"}}, agentadaptor.DecisionResponse{Result: agentadaptor.DecisionRejected})
	inner, _ := none["outcome"].(map[string]any)
	if inner["outcome"] != "cancelled" {
		t.Errorf("no matching reject option should cancel, got %v", none)
	}
}

func TestACPChunkText(t *testing.T) {
	got := acpChunkText([]byte(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}`))
	if got != "hello" {
		t.Errorf("chunk text = %q, want hello", got)
	}
}

func outcomeOptionID(m map[string]any) string {
	inner, _ := m["outcome"].(map[string]any)
	if inner == nil {
		return ""
	}
	s, _ := inner["optionId"].(string)
	return s
}

func TestParserForwardsUnknownAsRaw(t *testing.T) {
	// A non-JSON stdout line becomes a stdout transcript item.
	p := newParser(nil)
	_ = p.onChunk("stdout", []byte("plain log line\n"), timeNow())
	p.finalize()
	if len(p.transcript) == 0 || !strings.Contains(p.transcript[0].Text, "plain log line") {
		t.Errorf("expected stdout transcript, got %+v", p.transcript)
	}
}
