package codebuddy

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	driver "github.com/agent-dance/agent-adaptor/driver"
)

type controlTestSink struct {
	mu       sync.Mutex
	requests []driver.DecisionRequest
	respond  func(driver.DecisionRequest) driver.DecisionResponse
}

func (s *controlTestSink) Emit(driver.RunEvent) error            { return nil }
func (s *controlTestSink) EmitStream(driver.StreamPayload) error { return nil }
func (s *controlTestSink) RequestDecision(_ context.Context, req driver.DecisionRequest) (driver.DecisionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	return s.respond(req), nil
}

type controlTestStdin struct {
	mu     sync.Mutex
	frames [][]byte
	closed bool
}

func (s *controlTestStdin) Write(frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, append([]byte(nil), frame...))
	return nil
}
func (s *controlTestStdin) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func TestControlResultClosesStdin(t *testing.T) {
	stdin := &controlTestStdin{}
	p := newParser(&controlTestSink{respond: func(driver.DecisionRequest) driver.DecisionResponse {
		return driver.DecisionResponse{Result: driver.DecisionApproved}
	}})
	p.enableControl(context.Background(), &controlTestSink{respond: func(driver.DecisionRequest) driver.DecisionResponse {
		return driver.DecisionResponse{Result: driver.DecisionApproved}
	}}, stdin, "run-1", driver.HumanDecisionPolicy{}, "prompt")
	p.handleResult(`{"type":"result","subtype":"success","result":"done"}`, map[string]any{"result": "done"}, "success")
	if !stdin.closed {
		t.Fatal("terminal control result must close stdin")
	}
}

func TestWantsControlTransport(t *testing.T) {
	cases := []struct {
		name string
		p    driver.HumanDecisionPolicy
		want bool
	}{
		{name: "empty policy remains batch", want: false},
		{name: "permission ask", p: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk}, want: true},
		{name: "plan review ask", p: driver.HumanDecisionPolicy{PlanReview: driver.HumanDecisionAsk}, want: true},
		{name: "plan review auto approve", p: driver.HumanDecisionPolicy{PlanReview: driver.HumanDecisionAutoApprove}, want: true},
		{name: "question ask", p: driver.HumanDecisionPolicy{Question: driver.QuestionAsk}, want: true},
		{name: "permission auto reject", p: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoReject}, want: true},
		{name: "auto approve remains batch", p: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsControlTransport(tc.p); got != tc.want {
				t.Fatalf("wantsControlTransport(%+v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestBuildExecArgsControl(t *testing.T) {
	req := driver.Request{
		Session: &driver.SessionContext{
			State: &driver.SessionState{ResumeID: "session-1"},
		},
	}
	args := buildExecArgs(
		Config{Model: "glm", Effort: "high", MaxTurnsPerRun: 3},
		req,
		PermissionUnset,
		true,
	)
	for _, want := range []string{"--input-format=stream-json", "--output-format=stream-json", "--verbose", "--include-partial-messages", "--resume", "session-1"} {
		if !hasArg(args, want) {
			t.Fatalf("control args missing %q: %v", want, args)
		}
	}
	for _, forbidden := range []string{"--print", "--acp", "--acp-transport", "--setting-sources", "none"} {
		if hasArg(args, forbidden) {
			t.Fatalf("control args contain forbidden %q: %v", forbidden, args)
		}
	}
}

func TestBuildExecArgsControlDropsConflictingExtraArgs(t *testing.T) {
	args := buildExecArgs(Config{
		CommonConfig: CommonConfig{
			ExtraArgs: []string{"--acp", "--custom-after-bool", "--acp-transport", "stdio", "--print", "--setting-sources", "none", "--permission-mode", "plan", "--dangerously-skip-permissions", "-y", "--custom-flag"},
		},
	}, driver.Request{}, PermissionUnset, true)
	for _, forbidden := range []string{"--acp", "--acp-transport", "--print", "--setting-sources", "--permission-mode", "--dangerously-skip-permissions", "-y"} {
		if hasArg(args, forbidden) {
			t.Fatalf("control args contain forbidden %q: %v", forbidden, args)
		}
	}
	if !hasArg(args, "--custom-flag") {
		t.Fatalf("control args dropped safe extra argument: %v", args)
	}
	if !hasArg(args, "--custom-after-bool") {
		t.Fatalf("control args consumed argument following boolean flag: %v", args)
	}
}

func TestBuildExecArgsHeadlessPolicyDropsConflictingExtraArgs(t *testing.T) {
	args := buildExecArgs(Config{
		CommonConfig: CommonConfig{
			ExtraArgs: []string{"--permission-mode", "plan", "--dangerously-skip-permissions", "-y", "--custom-flag"},
		},
	}, driver.Request{}, PermissionBypass)
	if got := argIndex(args, "--permission-mode"); got < 0 || got+1 >= len(args) || args[got+1] != string(PermissionBypass) {
		t.Fatalf("canonical permission mode missing: %v", args)
	}
	count := 0
	for _, arg := range args {
		if arg == "--permission-mode" || strings.HasPrefix(arg, "--permission-mode=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("permission mode count = %d, want 1: %v", count, args)
	}
	for _, forbidden := range []string{"plan", "--dangerously-skip-permissions", "-y"} {
		if hasArg(args, forbidden) {
			t.Fatalf("headless args contain conflicting %q: %v", forbidden, args)
		}
	}
	if !hasArg(args, "--custom-flag") {
		t.Fatalf("headless args dropped safe extra argument: %v", args)
	}
}

func TestControlRequestRoutesPlanReviewWithObservedPlan(t *testing.T) {
	sink := &controlTestSink{respond: func(req driver.DecisionRequest) driver.DecisionResponse {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionApproved}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", driver.HumanDecisionPolicy{PlanReview: driver.HumanDecisionAsk}, "prompt")

	write := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","id":"tool-write","input":{"file_path":"/tmp/.codebuddy/plans/example.md","content":"# Actual plan"}}]}}` + "\n")
	exitPlan := []byte(`{"type":"control_request","request_id":"request-1","request":{"subtype":"can_use_tool","tool_name":"ExitPlanMode","tool_use_id":"tool-plan","input":{}}}` + "\n")
	if err := p.onChunk("stdout", append(write, exitPlan...), timeNow()); err != nil {
		t.Fatal(err)
	}
	if len(sink.requests) != 1 {
		t.Fatalf("decision calls = %d, want 1", len(sink.requests))
	}
	req := sink.requests[0]
	if req.RequestID != "request-1" || req.Kind != driver.HumanDecisionPlanReview || req.Payload["plan"] != "# Actual plan" {
		t.Fatalf("plan request = %+v", req)
	}
	if len(stdin.frames) != 1 {
		t.Fatalf("response frames = %d, want 1", len(stdin.frames))
	}
	var response struct {
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(stdin.frames[0], &response); err != nil {
		t.Fatal(err)
	}
	if response.Response.RequestID != "request-1" {
		t.Fatalf("response correlation = %#v", response)
	}
}

func TestIsCodeBuddyPlanFile(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		configDir string
		want      bool
	}{
		{name: "default home layout", path: "/Users/x/.codebuddy/plans/a.md", configDir: "", want: true},
		{name: "custom config dir match", path: "/var/folders/tmp/002/plans/a.md", configDir: "/var/folders/tmp/002", want: true},
		{name: "custom config dir with /private prefix", path: "/private/var/folders/tmp/002/plans/a.md", configDir: "/var/folders/tmp/002", want: true},
		{name: "custom config dir trailing slash", path: "/cfg/plans/a.md", configDir: "/cfg/", want: true},
		{name: "custom config dir but default path still matches fallback", path: "/home/.codebuddy/plans/a.md", configDir: "/cfg", want: true},
		{name: "non-md file rejected", path: "/cfg/plans/a.txt", configDir: "/cfg", want: false},
		{name: "unrelated plans dir without config match", path: "/repo/docs/plans/a.md", configDir: "/cfg", want: false},
		{name: "empty config falls back and misses", path: "/var/folders/tmp/002/plans/a.md", configDir: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCodeBuddyPlanFile(tc.path, tc.configDir); got != tc.want {
				t.Fatalf("isCodeBuddyPlanFile(%q, %q) = %v, want %v", tc.path, tc.configDir, got, tc.want)
			}
		})
	}
}

// TestControlRoutesPlanReviewWithCustomConfigDir locks the fix: when
// CODEBUDDY_CONFIG_DIR points outside ~/.codebuddy, the plan file written under
// <configDir>/plans/*.md must still be captured and surfaced in Payload["plan"].
func TestControlRoutesPlanReviewWithCustomConfigDir(t *testing.T) {
	sink := &controlTestSink{respond: func(req driver.DecisionRequest) driver.DecisionResponse {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionApproved}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", driver.HumanDecisionPolicy{PlanReview: driver.HumanDecisionAsk}, "prompt")
	p.control.configDir = "/var/folders/tmp/002"

	write := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","id":"tool-write","input":{"file_path":"/var/folders/tmp/002/plans/quantum.md","content":"# Real plan"}}]}}` + "\n")
	exitPlan := []byte(`{"type":"control_request","request_id":"request-1","request":{"subtype":"can_use_tool","tool_name":"ExitPlanMode","tool_use_id":"tool-plan","input":{}}}` + "\n")
	if err := p.onChunk("stdout", append(write, exitPlan...), timeNow()); err != nil {
		t.Fatal(err)
	}
	if len(sink.requests) != 1 {
		t.Fatalf("decision calls = %d, want 1", len(sink.requests))
	}
	req := sink.requests[0]
	if req.Kind != driver.HumanDecisionPlanReview || req.Payload["plan"] != "# Real plan" {
		t.Fatalf("plan request = %+v", req)
	}
}

func TestControlQuestionWritesAnswersToUpdatedInput(t *testing.T) {
	sink := &controlTestSink{respond: func(req driver.DecisionRequest) driver.DecisionResponse {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAnswered, Answer: map[string]any{"Choose": "A"}}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", driver.HumanDecisionPolicy{Question: driver.QuestionAsk}, "prompt")
	line := []byte(`{"type":"control_request","request_id":"question-1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tool-question","input":{"questions":[{"question":"Choose","options":[{"label":"A"}]}]}}}` + "\n")
	if err := p.onChunk("stdout", line, timeNow()); err != nil {
		t.Fatal(err)
	}
	if len(sink.requests) != 1 || sink.requests[0].Kind != driver.HumanDecisionQuestion {
		t.Fatalf("question requests = %+v", sink.requests)
	}
	var frame struct {
		Response struct {
			Response struct {
				UpdatedInput map[string]any `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(stdin.frames[0], &frame); err != nil {
		t.Fatal(err)
	}
	if answers, ok := frame.Response.Response.UpdatedInput["answers"].(map[string]any); !ok || answers["Choose"] != "A" {
		t.Fatalf("updated answers = %#v", frame.Response.Response.UpdatedInput)
	}
}

func TestControlQuestionPassesNestedAnswerThroughUnchanged(t *testing.T) {
	answers := map[string]any{
		"第一题": "答案一",
		"第二题": "答案二",
		"第三题": "答案三",
	}
	want := map[string]any{"answers": answers}
	sink := &controlTestSink{respond: func(req driver.DecisionRequest) driver.DecisionResponse {
		return driver.DecisionResponse{
			RequestID: req.RequestID,
			Result:    driver.DecisionAnswered,
			Answer:    want,
		}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", driver.HumanDecisionPolicy{Question: driver.QuestionAsk}, "prompt")
	line := []byte(`{"type":"control_request","request_id":"question-1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tool-question","input":{"questions":[{"question":"第一题"},{"question":"第二题"},{"question":"第三题"}]}}}` + "\n")
	if err := p.onChunk("stdout", line, timeNow()); err != nil {
		t.Fatal(err)
	}

	var frame struct {
		Response struct {
			Response struct {
				UpdatedInput map[string]any `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(stdin.frames[0], &frame); err != nil {
		t.Fatal(err)
	}
	got, ok := frame.Response.Response.UpdatedInput["answers"].(map[string]any)
	if !ok {
		t.Fatalf("updated answers type = %T, want map: %#v", frame.Response.Response.UpdatedInput["answers"], frame.Response.Response.UpdatedInput)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updated answers = %#v, want %#v", got, want)
	}
}

func TestQuestionAnswersPreservesDirectMultiQuestionAnswers(t *testing.T) {
	input := map[string]any{
		"questions": []any{
			map[string]any{"question": "第一题"},
			map[string]any{"question": "第二题"},
			map[string]any{"question": "第三题"},
		},
	}
	want := map[string]any{
		"第一题": "答案一",
		"第二题": "答案二",
		"第三题": "答案三",
	}
	got := questionAnswers(input, driver.DecisionResponse{Answer: want})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("question answers = %#v, want %#v", got, want)
	}
}

func TestControlDenyInterruptHonorsPolicy(t *testing.T) {
	decodeInner := func(frame []byte) map[string]any {
		var env struct {
			Response struct {
				Response map[string]any `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(frame, &env); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		return env.Response.Response
	}
	cases := []struct {
		name          string
		policy        driver.HumanDecisionPolicy
		result        driver.DecisionResult
		wantInterrupt bool
	}{
		{
			name:          "reject default aborts",
			policy:        driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk},
			result:        driver.DecisionRejected,
			wantInterrupt: true,
		},
		{
			name:          "reject continue keeps run",
			policy:        driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk, OnReject: driver.FailureContinue},
			result:        driver.DecisionRejected,
			wantInterrupt: false,
		},
		{
			name:          "timeout honors on_timeout continue",
			policy:        driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk, OnTimeout: driver.FailureContinue},
			result:        driver.DecisionTimedOut,
			wantInterrupt: false,
		},
		{
			name:          "aborted default aborts",
			policy:        driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk},
			result:        driver.DecisionAborted,
			wantInterrupt: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &controlTestSink{respond: func(req driver.DecisionRequest) driver.DecisionResponse {
				return driver.DecisionResponse{RequestID: req.RequestID, Result: tc.result, Text: "nope"}
			}}
			stdin := &controlTestStdin{}
			p := newParser(sink)
			p.enableControl(context.Background(), sink, stdin, "run-1", tc.policy, "prompt")
			line := []byte(`{"type":"control_request","request_id":"perm-1","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tool-bash","input":{"command":"ls"}}}` + "\n")
			if err := p.onChunk("stdout", line, timeNow()); err != nil {
				t.Fatal(err)
			}
			if len(stdin.frames) != 1 {
				t.Fatalf("frames = %d, want 1", len(stdin.frames))
			}
			resp := decodeInner(stdin.frames[0])
			if allowed, _ := resp["allowed"].(bool); allowed {
				t.Fatalf("denied decision must not be allowed, resp=%#v", resp)
			}
			interrupt, ok := resp["interrupt"].(bool)
			if !ok {
				t.Fatalf("deny response must always carry interrupt bool, resp=%#v", resp)
			}
			if interrupt != tc.wantInterrupt {
				t.Fatalf("interrupt = %v, want %v (resp=%#v)", interrupt, tc.wantInterrupt, resp)
			}
		})
	}
}

// TestControlQuestionAnswersFromChoiceWhenAnswerMissing locks the fix for the
// hang reported when hosts (debug viewer / production web viewer) resolve an
// AskUserQuestion decision with only Choice set (no Answer) — a form that is
// explicitly legal and already handled by the claude driver's
// resolveSingleQuestionAnswer fallback. Previously
// codebuddy only trusted decision.Answer, so updatedInput.answers stayed nil
// no matter which choice the host picked, and the CLI never unblocked.
func TestControlQuestionAnswersFromChoiceWhenAnswerMissing(t *testing.T) {
	sink := &controlTestSink{respond: func(req driver.DecisionRequest) driver.DecisionResponse {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAnswered, Choice: "A"}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", driver.HumanDecisionPolicy{Question: driver.QuestionAsk}, "prompt")
	line := []byte(`{"type":"control_request","request_id":"question-1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tool-question","input":{"questions":[{"question":"Choose","options":[{"label":"A"},{"label":"B"}]}]}}}` + "\n")
	if err := p.onChunk("stdout", line, timeNow()); err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Response struct {
			Response struct {
				UpdatedInput map[string]any `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(stdin.frames[0], &frame); err != nil {
		t.Fatal(err)
	}
	answers, ok := frame.Response.Response.UpdatedInput["answers"].(map[string]any)
	if !ok || answers["Choose"] != "A" {
		t.Fatalf("updated answers = %#v, want {Choose: A}", frame.Response.Response.UpdatedInput)
	}
}

// TestControlQuestionAnswersFromChoiceFallsBackToRawValue covers a choice
// that doesn't match any known option label (defensive path): the raw
// choice string is still surfaced as the answer instead of null.
func TestControlQuestionAnswersFromChoiceFallsBackToRawValue(t *testing.T) {
	sink := &controlTestSink{respond: func(req driver.DecisionRequest) driver.DecisionResponse {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAnswered, Choice: "custom-value"}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", driver.HumanDecisionPolicy{Question: driver.QuestionAsk}, "prompt")
	line := []byte(`{"type":"control_request","request_id":"question-1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tool-question","input":{"questions":[{"question":"Choose","options":[{"label":"A"}]}]}}}` + "\n")
	if err := p.onChunk("stdout", line, timeNow()); err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Response struct {
			Response struct {
				UpdatedInput map[string]any `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(stdin.frames[0], &frame); err != nil {
		t.Fatal(err)
	}
	answers, ok := frame.Response.Response.UpdatedInput["answers"].(map[string]any)
	if !ok || answers["Choose"] != "custom-value" {
		t.Fatalf("updated answers = %#v, want {Choose: custom-value}", frame.Response.Response.UpdatedInput)
	}
}

func TestControlInitializeResponseStartsUserMessage(t *testing.T) {
	sink := &controlTestSink{respond: func(req driver.DecisionRequest) driver.DecisionResponse {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionApproved}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", driver.HumanDecisionPolicy{}, "hello")
	line := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"agent-adaptor-initialize","response":{}}}` + "\n")
	if err := p.onChunk("stdout", line, timeNow()); err != nil {
		t.Fatal(err)
	}
	if len(stdin.frames) != 1 {
		t.Fatalf("user frames = %d, want 1", len(stdin.frames))
	}
	var frame map[string]any
	if err := json.Unmarshal(stdin.frames[0], &frame); err != nil {
		t.Fatal(err)
	}
	if frame["type"] != "user" {
		t.Fatalf("user frame = %#v", frame)
	}
}
