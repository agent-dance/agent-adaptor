package a2a_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	a2ataskstore "github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
)

func TestAgentCardHandlerReturnsConfiguredCard(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(fakeRunner{}, a2a.ServerOptions{
		AgentCard: a2a.AgentCard{
			Name: "Bridge Agent", Description: "test", Version: "1.0.0", URL: "https://example.com/a2a",
			Skills: []a2a.Skill{{ID: "chat", Name: "Chat", Description: "chat"}},
		},
	})

	rec := httptest.NewRecorder()
	server.AgentCardHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var card struct {
		Name         string `json:"name"`
		Capabilities struct {
			Streaming bool `json:"streaming"`
		} `json:"capabilities"`
		SupportedInterfaces []struct {
			URL             string `json:"url"`
			ProtocolBinding string `json:"protocolBinding"`
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"supportedInterfaces"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.Name != "Bridge Agent" || !card.Capabilities.Streaming {
		t.Fatalf("unexpected card: %+v", card)
	}
	if len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].ProtocolVersion != "1.0" {
		t.Fatalf("interfaces = %+v", card.SupportedInterfaces)
	}
}

func TestAgentCardIntrospectionPreservesConfiguredDetails(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(fakeRunner{}, a2a.ServerOptions{
		AgentCard: a2a.AgentCard{
			Name:        "Bridge Agent",
			Description: "test",
			Version:     "1.0.0",
			URL:         "https://example.com/a2a",
			Provider:    &a2a.Provider{Organization: "Agent Dance", URL: "https://example.com"},
			Capabilities: a2a.Capabilities{
				Streaming: a2a.CapabilityEnabled,
				Extensions: []a2a.Extension{{
					URI: "https://example.com/ext", Description: "extension", Required: true,
					Params: map[string]any{"mode": "strict"},
				}},
			},
			Skills: []a2a.Skill{{
				ID: "chat", Name: "Chat", Description: "chat",
				Tags: []string{"coding"}, Examples: []string{"review this"},
				InputModes: []string{"text/plain"}, OutputModes: []string{"text/plain"},
			}},
			SecuritySchemes: []a2a.SecurityScheme{{
				Name: "bearer", Type: a2a.SecurityHTTP, Scheme: "Bearer", BearerFormat: "JWT",
			}},
			Security: []a2a.SecurityRequirement{{Schemes: map[string][]string{"bearer": {"task.write"}}}},
		},
	})

	card := server.AgentCard()
	if card.Provider == nil || card.Provider.Organization != "Agent Dance" {
		t.Fatalf("provider not preserved: %+v", card.Provider)
	}
	if card.Capabilities.Streaming != a2a.CapabilityEnabled || len(card.Capabilities.Extensions) != 1 {
		t.Fatalf("capabilities not preserved: %+v", card.Capabilities)
	}
	if len(card.Skills) != 1 || len(card.Skills[0].Tags) != 1 || len(card.Skills[0].Examples) != 1 || len(card.Skills[0].InputModes) != 1 {
		t.Fatalf("skill details not preserved: %+v", card.Skills)
	}
	if len(card.SecuritySchemes) != 1 || card.SecuritySchemes[0].Name != "bearer" || card.SecuritySchemes[0].BearerFormat != "JWT" {
		t.Fatalf("security schemes not preserved: %+v", card.SecuritySchemes)
	}
	if len(card.Security) != 1 || fmt.Sprint(card.Security[0].Schemes["bearer"]) != "[task.write]" {
		t.Fatalf("security requirements not preserved: %+v", card.Security)
	}
}

func TestAgentCardHandlerSupportsStreamingFalse(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(fakeRunner{}, a2a.ServerOptions{
		AgentCard: a2a.AgentCard{
			Name: "Bridge Agent", Description: "test", Version: "1.0.0", URL: "https://example.com/a2a",
			Capabilities: a2a.Capabilities{Streaming: a2a.CapabilityDisabled},
			Skills:       []a2a.Skill{{ID: "chat", Name: "Chat", Description: "chat"}},
		},
	})

	rec := httptest.NewRecorder()
	server.AgentCardHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var card struct {
		Capabilities struct {
			Streaming bool `json:"streaming"`
		} `json:"capabilities"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.Capabilities.Streaming {
		t.Fatalf("streaming capability = true, want false")
	}
}

func TestNewServerRejectsMisconfiguredCapabilities(t *testing.T) {
	t.Parallel()

	baseCard := a2a.AgentCard{
		Name: "Bridge Agent", Description: "test", Version: "1.0.0", URL: "https://example.com/a2a",
		Skills: []a2a.Skill{{ID: "chat", Name: "Chat", Description: "chat"}},
	}

	tests := []struct {
		name string
		opts a2a.ServerOptions
		want string
	}{
		{
			name: "push capability without collaborators",
			opts: a2a.ServerOptions{
				AgentCard: func() a2a.AgentCard {
					card := baseCard
					card.Capabilities.PushNotifications = true
					return card
				}(),
			},
			want: "push notifications capability requires explicit PushNotifications support",
		},
		{
			name: "extended capability without collaborators",
			opts: a2a.ServerOptions{
				AgentCard: func() a2a.AgentCard {
					card := baseCard
					card.Capabilities.ExtendedAgentCard = true
					return card
				}(),
			},
			want: "extended agent card capability requires explicit ExtendedAgentCard support",
		},
		{
			name: "push collaborators without capability",
			opts: a2a.ServerOptions{
				AgentCard: baseCard,
				PushNotifications: &a2a.PushNotificationSupport{
					Store:  new(noopPushConfigStore),
					Sender: noopPushSender{},
				},
			},
			want: "push notifications support requires AgentCard.Capabilities.PushNotifications=true",
		},
		{
			name: "extended collaborators without capability",
			opts: a2a.ServerOptions{
				AgentCard: baseCard,
				ExtendedAgentCard: &a2a.ExtendedAgentCardSupport{
					Static: &baseCard,
				},
			},
			want: "extended agent card support requires AgentCard.Capabilities.ExtendedAgentCard=true",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected panic containing %q", tc.want)
				}
				if !strings.Contains(fmt.Sprint(r), tc.want) {
					t.Fatalf("panic = %v, want substring %q", r, tc.want)
				}
			}()
			_ = a2a.NewServer(fakeRunner{}, tc.opts)
		})
	}
}

func TestSendMessageMapsRunnerToA2ATask(t *testing.T) {
	t.Parallel()

	var starts atomic.Int32
	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		starts.Add(1)
		if prompt != "hello bridge" {
			t.Fatalf("prompt = %q", prompt)
		}
		h := newScriptedHandle("run-1")
		go func() {
			h.emit(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextContent, Delta: "hello"})
			h.finish(agentadaptor.RunResult{
				RunID: "run-1", DriverType: "fake", Output: "hello", Summary: "done",
				Result: map[string]any{"ok": true},
			}, nil)
		}()
		return h, nil
	}}, testOptions())

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope struct {
		Result struct {
			Task *struct {
				ID     string `json:"id"`
				Status struct {
					State string `json:"state"`
				} `json:"status"`
				Artifacts []taskArtifact `json:"artifacts"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("Start calls = %d", starts.Load())
	}
	if envelope.Result.Task == nil || envelope.Result.Task.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("task = %+v", envelope.Result.Task)
	}
	if len(envelope.Result.Task.Artifacts) == 0 {
		t.Fatalf("expected artifacts: %+v", envelope.Result.Task)
	}
}

func TestInvalidPromptRequestDoesNotStartRunner(t *testing.T) {
	t.Parallel()

	var starts atomic.Int32
	server := a2a.NewServer(scriptedRunner{start: func(context.Context, string, ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		starts.Add(1)
		return nil, errors.New("runner should not start")
	}}, testOptions())

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"bad","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"data":{"x":1}}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if starts.Load() != 0 {
		t.Fatalf("Start calls = %d, want 0", starts.Load())
	}
	if envelope.Error == nil || !strings.Contains(envelope.Error.Message, "no user text part") {
		t.Fatalf("unexpected error envelope: %+v", envelope.Error)
	}
}

func TestSendMessageDefaultExposureOmitsDiagnostics(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		h := newScriptedHandle("run-privacy")
		go func() {
			h.finish(agentadaptor.RunResult{
				RunID: "run-privacy", DriverType: "fake", Output: "hello", Summary: "safe summary",
				Metadata: map[string]string{"authorization": "Bearer super-secret"},
				Usage:    &agentadaptor.Usage{InputTokens: 12, OutputTokens: 34},
				Result:   map[string]any{"token": "secret-token"},
				Transcript: []agentadaptor.TranscriptItem{{
					Kind: agentadaptor.TranscriptAssistant, Text: "hidden transcript",
				}},
				RawStreams: &agentadaptor.RawStreams{Stdout: "Authorization: Bearer hidden"},
			}, nil)
		}()
		return h, nil
	}}, testOptions())

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope struct {
		Result struct {
			Task *struct {
				Artifacts []taskArtifact `json:"artifacts"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	artifact := findTaskArtifact(t, envelope.Result.Task.Artifacts, a2a.ArtifactAgentAdaptorResult)
	data := artifact.Parts[0].Data
	if got := data["summary"]; got != "safe summary" {
		t.Fatalf("summary = %#v", got)
	}
	for _, forbidden := range []string{"metadata", "usage", "result", "transcript", "raw_streams", "run_id", "driver_type"} {
		if _, ok := data[forbidden]; ok {
			t.Fatalf("unexpected diagnostic field %q in %+v", forbidden, data)
		}
	}
}

func TestSendMessageExposurePolicyRedactsDiagnostics(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		h := newScriptedHandle("run-redacted")
		go func() {
			h.finish(agentadaptor.RunResult{
				Output:  "hello",
				Summary: "safe summary",
				Metadata: map[string]string{
					"authorization": "Bearer super-secret",
					"trace_id":      "trace-123",
				},
				Usage: &agentadaptor.Usage{InputTokens: 12, OutputTokens: 34},
				Result: map[string]any{
					"headers": map[string]any{
						"Authorization": "Bearer sk-live-123",
						"X-Trace":       "trace-456",
					},
					"access_token": "opaque-token",
				},
				Transcript: []agentadaptor.TranscriptItem{{
					Kind: agentadaptor.TranscriptToolResult,
					Text: `Authorization: Bearer sk-protocol-456`,
					Data: map[string]any{"refresh_token": "refresh-secret"},
				}},
				RawStreams: &agentadaptor.RawStreams{
					Stdout: `{"authorization":"Bearer stdout-secret"}`,
					Stderr: "Bearer stderr-secret",
				},
			}, nil)
		}()
		return h, nil
	}}, a2a.ServerOptions{
		AgentCard: testOptions().AgentCard,
		Exposure: a2a.ExposurePolicy{
			Diagnostics: a2a.DiagnosticsPolicy{
				IncludeMetadata:       true,
				IncludeUsage:          true,
				IncludeProviderResult: true,
				IncludeTranscript:     true,
				IncludeRawStreams:     true,
			},
		},
	})

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope struct {
		Result struct {
			Task *struct {
				Artifacts []taskArtifact `json:"artifacts"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	artifact := findTaskArtifact(t, envelope.Result.Task.Artifacts, a2a.ArtifactAgentAdaptorResult)
	data := artifact.Parts[0].Data

	metadata := nestedMap(t, data["metadata"], "metadata")
	if metadata["authorization"] != "[REDACTED]" {
		t.Fatalf("metadata.authorization = %#v", metadata["authorization"])
	}
	if metadata["trace_id"] != "trace-123" {
		t.Fatalf("metadata.trace_id = %#v", metadata["trace_id"])
	}

	result := nestedMap(t, data["result"], "result")
	if result["access_token"] != "[REDACTED]" {
		t.Fatalf("result.access_token = %#v", result["access_token"])
	}
	headers := nestedMap(t, result["headers"], "result.headers")
	if headers["Authorization"] != "[REDACTED]" {
		t.Fatalf("result.headers.Authorization = %#v", headers["Authorization"])
	}
	if headers["X-Trace"] != "trace-456" {
		t.Fatalf("result.headers.X-Trace = %#v", headers["X-Trace"])
	}

	rawStreams := nestedMap(t, data["raw_streams"], "raw_streams")
	if rawStreams["stdout"] != `{"authorization":"[REDACTED]"}` {
		t.Fatalf("raw_streams.stdout = %#v", rawStreams["stdout"])
	}
	if rawStreams["stderr"] != "Bearer [REDACTED]" {
		t.Fatalf("raw_streams.stderr = %#v", rawStreams["stderr"])
	}

	transcript, ok := data["transcript"].([]any)
	if !ok || len(transcript) != 1 {
		t.Fatalf("transcript = %#v", data["transcript"])
	}
	item := nestedMap(t, transcript[0], "transcript[0]")
	if item["Text"] != "Authorization: [REDACTED]" {
		t.Fatalf("transcript[0].Text = %#v", item["Text"])
	}
	itemData := nestedMap(t, item["Data"], "transcript[0].Data")
	if itemData["refresh_token"] != "[REDACTED]" {
		t.Fatalf("transcript[0].Data.refresh_token = %#v", itemData["refresh_token"])
	}
}

func TestSendMessageRunStreamingDisabledSkipsSDKStreaming(t *testing.T) {
	t.Parallel()

	driver := &streamFlagDriver{}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(
		driver,
		struct{}{},
		agentadaptor.WithDefaultStreaming(),
	)))
	server := a2a.NewServer(sdk.Default(), a2a.ServerOptions{
		AgentCard:    testOptions().AgentCard,
		RunOptions:   []agentadaptor.RunOption{agentadaptor.WithStreaming()},
		RunStreaming: a2a.RunStreamingDisabled,
	})

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !driver.ran.Load() {
		t.Fatal("expected driver to run")
	}
	if driver.streaming.Load() {
		t.Fatal("expected SDK streaming to be disabled")
	}
}

func TestSendMessageSupportsLegacyPromptBuilderOption(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(scriptedRunner{start: func(_ context.Context, prompt string, _ ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		if prompt != "legacy prompt" {
			t.Fatalf("prompt = %q, want legacy prompt", prompt)
		}
		h := newScriptedHandle("run-legacy-prompt")
		go h.finish(agentadaptor.RunResult{RunID: "run-legacy-prompt", Output: "ok"}, nil)
		return h, nil
	}}, a2a.ServerOptions{
		AgentCard: testOptions().AgentCard,
		Prompt: a2a.PromptBuilderFunc(func(context.Context, a2a.InboundRequest) (string, []agentadaptor.RunOption, error) {
			return "legacy prompt", nil, nil
		}),
	})

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"ignored"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestSendMessageResultBuilderAppendsCustomArtifactsAndStatusText(t *testing.T) {
	t.Parallel()

	status := "custom final status"
	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		h := newScriptedHandle("run-custom")
		go func() {
			h.finish(agentadaptor.RunResult{
				RunID: "run-custom", DriverType: "fake", Output: "hello", Summary: "safe summary",
				StructuredOutput: &agentadaptor.StructuredOutput{
					Valid:   true,
					RawJSON: json.RawMessage(`{"state":"passed","description":"looks good"}`),
				},
			}, nil)
		}()
		return h, nil
	}}, a2a.ServerOptions{
		AgentCard: testOptions().AgentCard,
		ResultBuilder: a2a.ResultBuilderFunc(func(ctx context.Context, req a2a.InboundRequest, result agentadaptor.RunResult) (a2a.BuiltResult, error) {
			return a2a.BuiltResult{
				StatusText: &status,
				Artifacts: []a2a.ArtifactSpec{{
					Name: "review-result",
					Parts: []a2a.Part{
						{Kind: a2a.PartText, Text: "review summary"},
						{Kind: a2a.PartData, Data: map[string]any{"state": "passed", "description": "looks good"}},
					},
				}},
			}, nil
		}),
	})

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope struct {
		Result struct {
			Task *struct {
				Status struct {
					Message *struct {
						Parts []taskArtifactPart `json:"parts"`
					} `json:"message"`
				} `json:"status"`
				Artifacts []taskArtifact `json:"artifacts"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Result.Task == nil || envelope.Result.Task.Status.Message == nil || len(envelope.Result.Task.Status.Message.Parts) != 1 || envelope.Result.Task.Status.Message.Parts[0].Text != status {
		t.Fatalf("unexpected status message: %+v", envelope.Result.Task)
	}
	if findTaskArtifact(t, envelope.Result.Task.Artifacts, a2a.ArtifactAgentAdaptorResult).Name != a2a.ArtifactAgentAdaptorResult {
		t.Fatal("expected default agent-adaptor-result artifact")
	}
	custom := findTaskArtifact(t, envelope.Result.Task.Artifacts, "review-result")
	if len(custom.Parts) != 2 || custom.Parts[0].Text != "review summary" {
		t.Fatalf("unexpected custom artifact: %+v", custom)
	}
	if custom.Parts[1].Data["state"] != "passed" {
		t.Fatalf("unexpected custom artifact data: %+v", custom.Parts[1].Data)
	}
}

func TestSendMessageResultBuilderCanReplaceDefaultArtifacts(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		h := newScriptedHandle("run-replace")
		go func() {
			h.finish(agentadaptor.RunResult{
				RunID: "run-replace", DriverType: "fake", Output: "hello", Summary: "safe summary",
			}, nil)
		}()
		return h, nil
	}}, a2a.ServerOptions{
		AgentCard: testOptions().AgentCard,
		ResultBuilder: a2a.ResultBuilderFunc(func(ctx context.Context, req a2a.InboundRequest, result agentadaptor.RunResult) (a2a.BuiltResult, error) {
			return a2a.BuiltResult{
				ReplaceDefaultArtifacts: true,
				Artifacts: []a2a.ArtifactSpec{{
					Name:  "only-custom",
					Parts: []a2a.Part{{Kind: a2a.PartText, Text: "custom only"}},
				}},
			}, nil
		}),
	})

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope struct {
		Result struct {
			Task *struct {
				Artifacts []taskArtifact `json:"artifacts"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Result.Task.Artifacts) != 1 || envelope.Result.Task.Artifacts[0].Name != "only-custom" {
		t.Fatalf("expected only custom artifact, got %+v", envelope.Result.Task.Artifacts)
	}
}

func TestSendMessageFailureSkipsResultBuilderAndPreservesFailure(t *testing.T) {
	t.Parallel()

	var buildCalls atomic.Int32
	server := a2a.NewServer(scriptedRunner{start: func(context.Context, string, ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		h := newScriptedHandle("run-failure")
		go h.finish(agentadaptor.RunResult{
			RunID: "run-failure",
			Failure: &agentadaptor.RunFailure{
				Code:    agentadaptor.FailurePolicyError,
				Message: "original policy failure",
			},
		}, nil)
		return h, nil
	}}, a2a.ServerOptions{
		AgentCard: testOptions().AgentCard,
		ResultBuilder: a2a.ResultBuilderFunc(func(context.Context, a2a.InboundRequest, agentadaptor.RunResult) (a2a.BuiltResult, error) {
			buildCalls.Add(1)
			return a2a.BuiltResult{}, errors.New("builder should not run")
		}),
	})

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope struct {
		Result struct {
			Task *struct {
				Status struct {
					State   string `json:"state"`
					Message *struct {
						Parts []taskArtifactPart `json:"parts"`
					} `json:"message"`
				} `json:"status"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if buildCalls.Load() != 0 {
		t.Fatalf("ResultBuilder calls = %d, want 0", buildCalls.Load())
	}
	if envelope.Result.Task == nil || envelope.Result.Task.Status.State != "TASK_STATE_FAILED" {
		t.Fatalf("task = %+v", envelope.Result.Task)
	}
	if envelope.Result.Task.Status.Message == nil || len(envelope.Result.Task.Status.Message.Parts) != 1 || envelope.Result.Task.Status.Message.Parts[0].Text != "original policy failure" {
		t.Fatalf("failure message = %+v", envelope.Result.Task.Status.Message)
	}
}

func TestSendMessageResultBuilderRejectsInvalidArtifactData(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(scriptedRunner{start: func(context.Context, string, ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		h := newScriptedHandle("run-invalid-artifact")
		go h.finish(agentadaptor.RunResult{RunID: "run-invalid-artifact", Output: "ok"}, nil)
		return h, nil
	}}, a2a.ServerOptions{
		AgentCard: testOptions().AgentCard,
		ResultBuilder: a2a.ResultBuilderFunc(func(context.Context, a2a.InboundRequest, agentadaptor.RunResult) (a2a.BuiltResult, error) {
			return a2a.BuiltResult{Artifacts: []a2a.ArtifactSpec{{
				Name:  "invalid-data",
				Parts: []a2a.Part{{Kind: a2a.PartData, Data: math.NaN()}},
			}}}, nil
		}),
	})

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`)
	defer resp.Body.Close()
	var envelope struct {
		Result struct {
			Task *struct {
				Status struct {
					State   string `json:"state"`
					Message *struct {
						Parts []taskArtifactPart `json:"parts"`
					} `json:"message"`
				} `json:"status"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Result.Task == nil || envelope.Result.Task.Status.State != "TASK_STATE_FAILED" || envelope.Result.Task.Status.Message == nil {
		t.Fatalf("task = %#v", envelope.Result.Task)
	}
	if got := envelope.Result.Task.Status.Message.Parts[0].Text; !strings.Contains(got, "not JSON-compatible") {
		t.Fatalf("failure message = %q", got)
	}
}

func TestSendStreamingMessageEmitsOrderedUpdates(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		h := newScriptedHandle("run-stream")
		go func() {
			time.Sleep(20 * time.Millisecond)
			h.emit(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextContent, Delta: "hel"})
			h.emit(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextContent, Delta: "lo"})
			h.emit(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextEnd})
			h.finish(agentadaptor.RunResult{RunID: "run-stream", Output: "hello", Summary: "done"}, nil)
		}()
		return h, nil
	}}, testOptions())

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"stream","method":"SendStreamingMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"stream"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	frames := readSSEFrames(t, resp.Body, 2*time.Second)
	if len(frames) < 6 {
		t.Fatalf("frames = %d, want at least 6: %+v", len(frames), frames)
	}
	if frames[0].Result.Task == nil {
		t.Fatalf("first frame should be task: %+v", frames[0])
	}
	if frames[1].Result.StatusUpdate == nil || frames[1].Result.StatusUpdate.Status.State != "TASK_STATE_WORKING" {
		t.Fatalf("second frame should be working: %+v", frames[1])
	}
	if frames[2].Result.ArtifactUpdate == nil || frames[2].Result.ArtifactUpdate.Artifact.Name != a2a.ArtifactAssistantOutput || frames[2].Result.ArtifactUpdate.Artifact.Parts[0].Text != "hel" || frames[2].Result.ArtifactUpdate.LastChunk {
		t.Fatalf("third frame should be artifact hel: %+v", frames[2])
	}
	if frames[3].Result.ArtifactUpdate == nil || frames[3].Result.ArtifactUpdate.Artifact.Name != a2a.ArtifactAssistantOutput || frames[3].Result.ArtifactUpdate.Artifact.Parts[0].Text != "lo" || frames[3].Result.ArtifactUpdate.LastChunk {
		t.Fatalf("fourth frame should be artifact lo: %+v", frames[3])
	}
	if frames[4].Result.ArtifactUpdate == nil || frames[4].Result.ArtifactUpdate.Artifact.Name != a2a.ArtifactAssistantOutput || !frames[4].Result.ArtifactUpdate.LastChunk {
		t.Fatalf("fifth frame should close assistant-output: %+v", frames[4])
	}
	if frames[5].Result.ArtifactUpdate == nil || frames[5].Result.ArtifactUpdate.Artifact.Name != a2a.ArtifactAgentAdaptorResult || !frames[5].Result.ArtifactUpdate.LastChunk {
		t.Fatalf("sixth frame should close agent-adaptor-result: %+v", frames[5])
	}
	if frames[len(frames)-1].Result.StatusUpdate == nil || frames[len(frames)-1].Result.StatusUpdate.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("last frame should be completed: %+v", frames[len(frames)-1])
	}
}

func TestSendStreamingMessageStatusDataModeAvoidsIntermediateArtifacts(t *testing.T) {
	t.Parallel()
	opts := testOptions()
	opts.StreamWire = a2a.StreamWireStatusData
	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, runOpts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		h := newScriptedHandle("run-status-data")
		go func() {
			time.Sleep(20 * time.Millisecond)
			h.emit(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextStart, Sequence: 1, MessageID: "msg-1"})
			h.emit(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextContent, Sequence: 2, MessageID: "msg-1", Delta: "hello"})
			h.emit(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextEnd, Sequence: 3, MessageID: "msg-1"})
			h.finish(agentadaptor.RunResult{RunID: "run-status-data", Output: "hello", Summary: "done"}, nil)
		}()
		return h, nil
	}}, opts)

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"stream","method":"SendStreamingMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"stream"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	frames := readSSEFrames(t, resp.Body, 2*time.Second)
	var streamKinds []string
	for _, frame := range frames {
		if frame.Result.ArtifactUpdate != nil && frame.Result.ArtifactUpdate.Artifact.Name == a2a.ArtifactAssistantOutput {
			t.Fatalf("unexpected intermediate assistant artifact: %+v", frame)
		}
		if frame.Result.StatusUpdate == nil {
			continue
		}
		for _, part := range frame.Result.StatusUpdate.Status.Message.Parts {
			if part.Data["schema"] != a2a.AdapterStreamSchemaV1 {
				continue
			}
			event, _ := part.Data["event"].(map[string]any)
			streamKinds = append(streamKinds, fmt.Sprint(event["kind"]))
		}
	}
	want := []string{"text.start", "text.content", "text.end"}
	if fmt.Sprint(streamKinds) != fmt.Sprint(want) {
		t.Fatalf("stream kinds = %v, want %v, frames=%+v", streamKinds, want, frames)
	}
	card := server.AgentCard()
	foundExtension := false
	for _, extension := range card.Capabilities.Extensions {
		if extension.URI == a2a.AdapterStreamExtensionURI {
			foundExtension = true
		}
	}
	if !foundExtension {
		t.Fatalf("agent card extensions = %+v", card.Capabilities.Extensions)
	}
}

func TestCancelTaskCancelsUnderlyingRun(t *testing.T) {
	t.Parallel()

	handle := newScriptedHandle("run-cancel")
	runStarted := make(chan struct{})
	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		close(runStarted)
		return handle, nil
	}}, testOptions())

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"start","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"cancel"}]},"configuration":{"returnImmediately":true}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d", resp.StatusCode)
	}
	var started struct {
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if started.Result.Task.ID == "" {
		t.Fatal("missing task id")
	}
	select {
	case <-runStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start before cancellation")
	}

	cancel := postRPC(t, server.Handler(), fmt.Sprintf(`{"jsonrpc":"2.0","id":"cancel","method":"CancelTask","params":{"id":%q}}`, started.Result.Task.ID))
	defer cancel.Body.Close()
	if cancel.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", cancel.StatusCode)
	}
	if handle.cancelled.Load() != 1 {
		t.Fatalf("Cancel calls = %d", handle.cancelled.Load())
	}
	if handle.cancelHadDeadline.Load() != 1 {
		t.Fatal("Cancel context did not carry a deadline")
	}
	var cancelled struct {
		Result struct {
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"result"`
	}
	if err := json.NewDecoder(cancel.Body).Decode(&cancelled); err != nil {
		t.Fatalf("decode cancel: %v", err)
	}
	if cancelled.Result.Status.State != "TASK_STATE_CANCELED" {
		t.Fatalf("cancelled state = %q", cancelled.Result.Status.State)
	}
}

func TestNewServerUsesInjectedTaskStore(t *testing.T) {
	t.Parallel()

	store := &countingTaskStore{Store: a2ataskstore.NewInMemory(nil)}
	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		h := newScriptedHandle("run-store")
		go h.finish(agentadaptor.RunResult{RunID: "run-store", Output: "ok", Summary: "done"}, nil)
		return h, nil
	}}, a2a.ServerOptions{
		AgentCard:     testOptions().AgentCard,
		TaskLifecycle: a2a.TaskLifecycleOptions{Store: store},
	})

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if store.creates.Load() == 0 {
		t.Fatal("expected injected task store to receive Create")
	}
}

func TestCancelTaskCancelsStartupContextBeforeRunnerReturns(t *testing.T) {
	t.Parallel()

	startEntered := make(chan struct{})
	startCancelled := make(chan struct{})
	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		close(startEntered)
		<-ctx.Done()
		close(startCancelled)
		return nil, ctx.Err()
	}}, testOptions())

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"start","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"cancel startup"}]},"configuration":{"returnImmediately":true}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d", resp.StatusCode)
	}
	var started struct {
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if started.Result.Task.ID == "" {
		t.Fatal("missing task id")
	}
	select {
	case <-startEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("runner Start was not invoked")
	}

	cancel := postRPC(t, server.Handler(), fmt.Sprintf(`{"jsonrpc":"2.0","id":"cancel","method":"CancelTask","params":{"id":%q}}`, started.Result.Task.ID))
	defer cancel.Body.Close()
	if cancel.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", cancel.StatusCode)
	}
	select {
	case <-startCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("runner Start context was not cancelled")
	}
}

func testOptions() a2a.ServerOptions {
	return a2a.ServerOptions{AgentCard: a2a.AgentCard{
		Name: "Test Agent", Description: "test", Version: "1.0.0", URL: "https://example.com/a2a",
		Skills: []a2a.Skill{{ID: "chat", Name: "Chat", Description: "chat"}},
	}}
}

type scriptedRunner struct {
	start func(context.Context, string, ...agentadaptor.RunOption) (agentadaptor.RunHandle, error)
}

func (r scriptedRunner) Run(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunResult, error) {
	handle, err := r.Start(ctx, prompt, opts...)
	if err != nil {
		return agentadaptor.RunResult{}, err
	}
	return handle.Wait(ctx)
}

func (r scriptedRunner) Start(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
	if r.start == nil {
		return nil, errors.New("unexpected start")
	}
	return r.start(ctx, prompt, opts...)
}

type fakeRunner struct{}

func (fakeRunner) Run(context.Context, string, ...agentadaptor.RunOption) (agentadaptor.RunResult, error) {
	return agentadaptor.RunResult{}, nil
}

func (fakeRunner) Start(context.Context, string, ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
	return nil, errors.New("not implemented")
}

type streamFlagDriver struct {
	ran       atomic.Bool
	streaming atomic.Bool
}

func (d *streamFlagDriver) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{Type: "stream-flag", DisplayName: "Stream Flag"}
}

func (d *streamFlagDriver) ValidateConfig(any) error { return nil }

func (d *streamFlagDriver) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	d.ran.Store(true)
	d.streaming.Store(req.Streaming)
	return agentadaptor.DriverRunResult{
		Output:  "ok",
		Summary: "ok",
	}, nil
}

type scriptedHandle struct {
	runID             string
	stream            chan agentadaptor.StreamPayload
	done              chan waitResult
	once              sync.Once
	cancelled         atomic.Int32
	cancelHadDeadline atomic.Int32
}

type waitResult struct {
	result agentadaptor.RunResult
	err    error
}

func newScriptedHandle(runID string) *scriptedHandle {
	return &scriptedHandle{runID: runID, stream: make(chan agentadaptor.StreamPayload, 16), done: make(chan waitResult, 1)}
}

func (h *scriptedHandle) Events() <-chan agentadaptor.RunEvent {
	ch := make(chan agentadaptor.RunEvent)
	close(ch)
	return ch
}
func (h *scriptedHandle) StreamEvents() <-chan agentadaptor.StreamPayload { return h.stream }
func (h *scriptedHandle) RunID() string                                   { return h.runID }
func (h *scriptedHandle) DecisionRequests() <-chan agentadaptor.DecisionRequest {
	ch := make(chan agentadaptor.DecisionRequest)
	close(ch)
	return ch
}
func (h *scriptedHandle) ResolveDecision(string, agentadaptor.DecisionResponse) error { return nil }
func (h *scriptedHandle) Wait(ctx context.Context) (agentadaptor.RunResult, error) {
	select {
	case <-ctx.Done():
		return agentadaptor.RunResult{}, ctx.Err()
	case r := <-h.done:
		return r.result, r.err
	}
}
func (h *scriptedHandle) Cancel(ctx context.Context) error {
	h.once.Do(func() {
		h.cancelled.Add(1)
		if _, ok := ctx.Deadline(); ok {
			h.cancelHadDeadline.Store(1)
		}
		close(h.stream)
		h.done <- waitResult{err: context.Canceled}
	})
	return nil
}
func (h *scriptedHandle) emit(p agentadaptor.StreamPayload) { h.stream <- p }
func (h *scriptedHandle) finish(result agentadaptor.RunResult, err error) {
	h.once.Do(func() {
		close(h.stream)
		h.done <- waitResult{result: result, err: err}
	})
}

type countingTaskStore struct {
	a2ataskstore.Store
	creates atomic.Int32
}

func (s *countingTaskStore) Create(ctx context.Context, task *a2aproto.Task) (a2ataskstore.TaskVersion, error) {
	s.creates.Add(1)
	return s.Store.Create(ctx, task)
}

type noopPushConfigStore struct{}

func (*noopPushConfigStore) Save(context.Context, a2aproto.TaskID, *a2aproto.PushConfig) (*a2aproto.PushConfig, error) {
	return nil, nil
}
func (*noopPushConfigStore) Get(context.Context, a2aproto.TaskID, string) (*a2aproto.PushConfig, error) {
	return nil, nil
}
func (*noopPushConfigStore) List(context.Context, a2aproto.TaskID) ([]*a2aproto.PushConfig, error) {
	return nil, nil
}
func (*noopPushConfigStore) Delete(context.Context, a2aproto.TaskID, string) error { return nil }
func (*noopPushConfigStore) DeleteAll(context.Context, a2aproto.TaskID) error      { return nil }

type noopPushSender struct{}

func (noopPushSender) SendPush(context.Context, *a2aproto.PushConfig, a2aproto.Event) error {
	return nil
}

func postRPC(t *testing.T, handler http.Handler, body string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post RPC: %v", err)
	}
	return resp
}

type sseFrame struct {
	Result struct {
		Task *struct {
			ID string `json:"id"`
		} `json:"task"`
		StatusUpdate *struct {
			Status struct {
				State   string `json:"state"`
				Message struct {
					Parts []struct {
						Text string         `json:"text"`
						Data map[string]any `json:"data"`
					} `json:"parts"`
				} `json:"message"`
			} `json:"status"`
		} `json:"statusUpdate"`
		ArtifactUpdate *struct {
			Append    bool `json:"append"`
			LastChunk bool `json:"lastChunk"`
			Artifact  struct {
				ID    string `json:"artifactId"`
				Name  string `json:"name"`
				Parts []struct {
					Text string         `json:"text"`
					Data map[string]any `json:"data"`
				} `json:"parts"`
			} `json:"artifact"`
		} `json:"artifactUpdate"`
	} `json:"result"`
}

type taskArtifact struct {
	Name  string             `json:"name"`
	Parts []taskArtifactPart `json:"parts"`
}

type taskArtifactPart struct {
	Text string         `json:"text"`
	Data map[string]any `json:"data"`
}

func readSSEFrames(t *testing.T, body io.Reader, timeout time.Duration) []sseFrame {
	t.Helper()
	done := make(chan []sseFrame, 1)
	go func() {
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var frames []sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame sseFrame
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err == nil {
				frames = append(frames, frame)
			}
		}
		done <- frames
	}()
	select {
	case frames := <-done:
		return frames
	case <-time.After(timeout):
		t.Fatal("timed out reading SSE")
		return nil
	}
}

func findTaskArtifact(t *testing.T, artifacts []taskArtifact, name string) taskArtifact {
	t.Helper()
	for _, artifact := range artifacts {
		if artifact.Name == name {
			return artifact
		}
	}
	t.Fatalf("artifact %q not found in %+v", name, artifacts)
	var zero taskArtifact
	return zero
}

func nestedMap(t *testing.T, value any, path string) map[string]any {
	t.Helper()
	out, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v", path, value)
	}
	return out
}
