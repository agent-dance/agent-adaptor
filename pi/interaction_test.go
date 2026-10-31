package pi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

type interactionSink struct {
	testutil.EventRecorder
	decide func(context.Context, driver.DecisionRequest) (driver.DecisionResponse, error)
}

func (s *interactionSink) RequestDecision(ctx context.Context, req driver.DecisionRequest) (driver.DecisionResponse, error) {
	return s.decide(ctx, req)
}

func TestPiExtensionDecisionRouting(t *testing.T) {
	for _, name := range []string{"read", reviewPlanTool, askUserTool} {
		t.Run(name, func(t *testing.T) {
			ctx, abort := context.WithCancelCause(context.Background())
			defer abort(nil)
			var calls atomic.Int32
			sink := &interactionSink{decide: func(_ context.Context, req driver.DecisionRequest) (driver.DecisionResponse, error) {
				calls.Add(1)
				wantKind := driver.HumanDecisionPermission
				if name == reviewPlanTool {
					wantKind = driver.HumanDecisionPlanReview
				}
				if name == askUserTool {
					wantKind = driver.HumanDecisionQuestion
				}
				if req.Kind != wantKind || req.ToolCallID != "call-1" || req.RunID != "run-1" || req.Prompt == "" {
					t.Errorf("request=%+v", req)
				}
				if name == askUserTool && len(req.Choices) != 2 {
					t.Error("choices lost")
				}
				return driver.DecisionResponse{Result: driver.DecisionAnswered, Text: "B", Answer: map[string]any{"choice": "B"}}, nil
			}}
			req := driver.Request{RunID: "run-1", Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk}}}
			allow := []string{"read"}
			_, env, closeBridge, err := prepareExtension(ctx, req, nil, t.TempDir(), &allow, sink, abort)
			if err != nil {
				t.Fatal(err)
			}
			defer closeBridge()
			endpoint, token := interactionEndpoint(env)
			raw, _ := json.Marshal(extensionDecision{ToolCallID: "call-1", ToolName: name, Input: map[string]any{"plan": "Review this plan", "question": "Which?", "options": []string{"A", "B"}}})
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/decision", strings.NewReader(string(raw)))
			request.Header.Set("Authorization", "Bearer "+token)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != 200 || calls.Load() != 1 || !strings.Contains(string(body), `"answer":{"choice":"B"}`) {
				t.Fatalf("response=%s calls=%d", body, calls.Load())
			}
			if len(allow) != 3 {
				t.Fatal("selected native tools excluded decision tools")
			}
		})
	}
}

func interactionEndpoint(env []driver.EnvBinding) (url, token string) {
	for _, binding := range env {
		if strings.HasPrefix(binding.Name, "AGENT_ADAPTOR_PI_MCP_URL_") {
			url = binding.Value
		}
		if strings.HasPrefix(binding.Name, "AGENT_ADAPTOR_PI_MCP_TOKEN_") {
			token = binding.Value
		}
	}
	return
}

func TestPiDecisionAbortCancelsInvocation(t *testing.T) {
	ctx, abort := context.WithCancelCause(context.Background())
	defer abort(nil)
	want := errors.New("core policy rejected")
	sink := &interactionSink{decide: func(context.Context, driver.DecisionRequest) (driver.DecisionResponse, error) {
		return driver.DecisionResponse{}, want
	}}
	var allow []string
	_, env, closeBridge, err := prepareExtension(ctx, driver.Request{Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk}}}, nil, t.TempDir(), &allow, sink, abort)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBridge()
	endpoint, token := interactionEndpoint(env)
	request, _ := http.NewRequest(http.MethodPost, endpoint+"/decision", strings.NewReader(`{"toolCallId":"x","toolName":"read","input":{}}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !errors.Is(context.Cause(ctx), want) || response.StatusCode != http.StatusConflict {
		t.Fatalf("cause=%v status=%d", context.Cause(ctx), response.StatusCode)
	}
}

func TestPiInteractiveRequiresCapableSink(t *testing.T) {
	ctx, abort := context.WithCancelCause(context.Background())
	defer abort(nil)
	var allow []string
	_, _, _, err := prepareExtension(ctx, driver.Request{Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk}}}, nil, t.TempDir(), &allow, &testutil.EventRecorder{}, abort)
	if err == nil {
		t.Fatal("interactive policy silently ignored")
	}
}
