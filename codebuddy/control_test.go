package codebuddy

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type controlTestSink struct {
	mu       sync.Mutex
	requests []agentadaptor.DecisionRequest
	respond  func(agentadaptor.DecisionRequest) agentadaptor.DecisionResponse
}

func (s *controlTestSink) Emit(agentadaptor.RunEvent) error            { return nil }
func (s *controlTestSink) EmitStream(agentadaptor.StreamPayload) error { return nil }
func (s *controlTestSink) RequestDecision(_ context.Context, req agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
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
	p := newParser(&controlTestSink{respond: func(agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		return agentadaptor.DecisionResponse{Result: agentadaptor.DecisionApproved}
	}})
	p.enableControl(context.Background(), &controlTestSink{respond: func(agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		return agentadaptor.DecisionResponse{Result: agentadaptor.DecisionApproved}
	}}, stdin, "run-1", agentadaptor.HumanDecisionPolicy{}, "prompt")
	p.handleResult(`{"type":"result","subtype":"success","result":"done"}`, map[string]any{"result": "done"}, "success")
	if !stdin.closed {
		t.Fatal("terminal control result must close stdin")
	}
}

func TestWantsControlTransport(t *testing.T) {
	cases := []struct {
		name string
		p    agentadaptor.HumanDecisionPolicy
		want bool
	}{
		{name: "empty policy remains batch", want: false},
		{name: "permission ask", p: agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk}, want: true},
		{name: "plan review ask", p: agentadaptor.HumanDecisionPolicy{PlanReview: agentadaptor.HumanDecisionAsk}, want: true},
		{name: "question ask", p: agentadaptor.HumanDecisionPolicy{Question: agentadaptor.QuestionAsk}, want: true},
		{name: "permission auto reject", p: agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAutoReject}, want: true},
		{name: "auto approve remains batch", p: agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAutoApprove}, want: false},
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
	req := agentadaptor.DriverRunRequest{
		Session: &agentadaptor.DriverSessionContext{
			State: &agentadaptor.DriverSessionState{ResumeID: "session-1"},
		},
	}
	args := buildExecArgs(
		agentadaptor.CodeBuddyConfig{Model: "glm", Effort: "high", MaxTurnsPerRun: 3},
		req,
		agentadaptor.CodeBuddyPermissionUnset,
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
	args := buildExecArgs(agentadaptor.CodeBuddyConfig{
		CommonConfig: agentadaptor.CommonConfig{
			ExtraArgs: []string{"--acp", "--acp-transport", "stdio", "--print", "--setting-sources", "none", "--custom-flag"},
		},
	}, agentadaptor.DriverRunRequest{}, agentadaptor.CodeBuddyPermissionUnset, true)
	for _, forbidden := range []string{"--acp", "--acp-transport", "--print", "--setting-sources"} {
		if hasArg(args, forbidden) {
			t.Fatalf("control args contain forbidden %q: %v", forbidden, args)
		}
	}
	if !hasArg(args, "--custom-flag") {
		t.Fatalf("control args dropped safe extra argument: %v", args)
	}
}

func TestControlRequestRoutesPlanReviewWithObservedPlan(t *testing.T) {
	sink := &controlTestSink{respond: func(req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		return agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: agentadaptor.DecisionApproved}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", agentadaptor.HumanDecisionPolicy{PlanReview: agentadaptor.HumanDecisionAsk}, "prompt")

	write := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","id":"tool-write","input":{"file_path":"/tmp/.codebuddy/plans/example.md","content":"# Actual plan"}}]}}` + "\n")
	exitPlan := []byte(`{"type":"control_request","request_id":"request-1","request":{"subtype":"can_use_tool","tool_name":"ExitPlanMode","tool_use_id":"tool-plan","input":{}}}` + "\n")
	if err := p.onChunk("stdout", append(write, exitPlan...), timeNow()); err != nil {
		t.Fatal(err)
	}
	if len(sink.requests) != 1 {
		t.Fatalf("decision calls = %d, want 1", len(sink.requests))
	}
	req := sink.requests[0]
	if req.RequestID != "request-1" || req.Kind != agentadaptor.HumanDecisionPlanReview || req.Payload["plan"] != "# Actual plan" {
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
	sink := &controlTestSink{respond: func(req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		return agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: agentadaptor.DecisionApproved}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", agentadaptor.HumanDecisionPolicy{PlanReview: agentadaptor.HumanDecisionAsk}, "prompt")
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
	if req.Kind != agentadaptor.HumanDecisionPlanReview || req.Payload["plan"] != "# Real plan" {
		t.Fatalf("plan request = %+v", req)
	}
}

func TestControlQuestionWritesAnswersToUpdatedInput(t *testing.T) {
	sink := &controlTestSink{respond: func(req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		return agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: agentadaptor.DecisionAnswered, Answer: map[string]any{"Choose": "A"}}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", agentadaptor.HumanDecisionPolicy{Question: agentadaptor.QuestionAsk}, "prompt")
	line := []byte(`{"type":"control_request","request_id":"question-1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tool-question","input":{"questions":[{"question":"Choose","options":[{"label":"A"}]}]}}}` + "\n")
	if err := p.onChunk("stdout", line, timeNow()); err != nil {
		t.Fatal(err)
	}
	if len(sink.requests) != 1 || sink.requests[0].Kind != agentadaptor.HumanDecisionQuestion {
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
		policy        agentadaptor.HumanDecisionPolicy
		result        agentadaptor.DecisionResult
		wantInterrupt bool
	}{
		{
			name:          "reject default aborts",
			policy:        agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk},
			result:        agentadaptor.DecisionRejected,
			wantInterrupt: true,
		},
		{
			name:          "reject continue keeps run",
			policy:        agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk, OnReject: agentadaptor.FailureContinue},
			result:        agentadaptor.DecisionRejected,
			wantInterrupt: false,
		},
		{
			name:          "timeout honors on_timeout continue",
			policy:        agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk, OnTimeout: agentadaptor.FailureContinue},
			result:        agentadaptor.DecisionTimedOut,
			wantInterrupt: false,
		},
		{
			name:          "aborted default aborts",
			policy:        agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk},
			result:        agentadaptor.DecisionAborted,
			wantInterrupt: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &controlTestSink{respond: func(req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
				return agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: tc.result, Text: "nope"}
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

func TestControlInitializeResponseStartsUserMessage(t *testing.T) {
	sink := &controlTestSink{respond: func(req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		return agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: agentadaptor.DecisionApproved}
	}}
	stdin := &controlTestStdin{}
	p := newParser(sink)
	p.enableControl(context.Background(), sink, stdin, "run-1", agentadaptor.HumanDecisionPolicy{}, "hello")
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
