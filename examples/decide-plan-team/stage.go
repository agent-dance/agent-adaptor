package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

const (
	designerAgentKey                 = "designer"
	designPlanToolName               = "delegate_to_plan_designer"
	designPlanStage                  = "design_plan"
	designPlanArtifact               = "design-plan-artifact"
	implementerAgentKey              = "implementer"
	implementRequirementToolName     = "delegate_to_requirement_implementer"
	implementRequirementStage        = "implement_requirement"
	implementRequirementArtifactName = "implement-requirement-artifact"
	verifierAgentKey                 = "verifier"
	verifyCodeToolName               = "delegate_to_code_verifier"
	verifyCodeStage                  = "verify_code"
	verifyCodeArtifactName           = "verify-code-artifact"
)

// InvocationContext 携带这次阶段调用绑定的工作流元信息。
type InvocationContext struct {
	WorkflowRunID string
	StepID        string
	Attempt       int
}

// DemoTaskContext 保存这次 demo 已知的固定输入。
//
// 这些字段由 host 预先知道，因此不需要 leader 在每次调用工具时重复填写。
type DemoTaskContext struct {
	RequirementTitle          string
	RequirementContent        string
	RequirementAttachmentPath string
	PrototypePath             string
	WorkspacePath             string
}

// demoWorkflowState 保存多阶段之间由 host 托管的共享状态。
//
// 当前示例里主要是把 planning 阶段产出的 plan_content 缓存在这里，
// 这样实现阶段就不需要 leader 再手工把结构化字段重新拼一遍。
type demoWorkflowState struct {
	mu             sync.Mutex
	latestPlanText string
}

// SavePlanContent 记录最新的 plan_content。
func (s *demoWorkflowState) SavePlanContent(plan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latestPlanText = strings.TrimSpace(plan)
}

// LoadPlanContent 读取最新的 plan_content。
func (s *demoWorkflowState) LoadPlanContent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.latestPlanText)
}

// designPlanToolInput 是 leader 调 planning 工具时填写的文本指令。
//
// host 会负责补齐全部结构化参数；leader 只需要给一段自然语言指令。
type designPlanToolInput struct {
	Instruction string `json:"instruction"`
}

// implementRequirementToolInput 是 leader 调实现工具时填写的文本指令。
//
// 真正的 plan_content 和 workspace_path 由 host 负责补齐。
type implementRequirementToolInput struct {
	Instruction string `json:"instruction"`
}

// verifyCodeToolInput 是 leader 调验证工具时填写的文本指令。
//
// 编译和单测对应的结构化参数由 host 负责补齐。
type verifyCodeToolInput struct {
	Instruction string `json:"instruction"`
}

// newDesignPlanTool 返回 leader 侧真正挂到 MCP 上的 `delegate_to_plan_designer` 工具。
//
// 你可以把它理解成：
// 1. leader 只给一段自然语言指令
// 2. host 把固定结构化字段补齐成 Team Mode 的 request
// 3. 把远端返回的 artifact 再解成完整制品内容（TextPart + DataPart）
func newDesignPlanTool(task DemoTaskContext, state *demoWorkflowState, inv InvocationContext) a2adelegation.ToolSpec {
	return a2adelegation.ToolSpec{
		Name:        designPlanToolName,
		Description: "Ask the plan designer to propose a very small implementation plan for the current requirement.",
		InputSchema: designPlanInputSchema(),
		BuildRequest: func(ctx context.Context, raw json.RawMessage, env a2adelegation.ToolContext) (a2adelegation.DelegationRequest, error) {
			input, err := decodeDesignPlanToolRequest(raw)
			if err != nil {
				return a2adelegation.DelegationRequest{}, err
			}
			req := DesignPlanRequest{
				RequirementTitle:          task.RequirementTitle,
				RequirementContent:        task.RequirementContent,
				RequirementAttachmentPath: task.RequirementAttachmentPath,
				PrototypePath:             task.PrototypePath,
				WorkspacePath:             task.WorkspacePath,
			}
			return a2adelegation.DelegationRequest{
				RunID:                  env.RunID,
				ParentToolCallID:       env.ParentToolCallID,
				Agent:                  designerAgentKey,
				Message:                buildDesignPlanMessage(input.Instruction, req),
				IncludeRemoteArtifacts: true,
				Tenant:                 env.Tenant,
				StageContext: a2adelegation.DelegationStageContext{
					WorkflowRunID: inv.WorkflowRunID,
					Stage:         designPlanStage,
					StepID:        inv.StepID,
					Attempt:       inv.Attempt,
				},
			}, nil
		},
		BuildResult: func(ctx context.Context, out a2adelegation.DelegationResult) (any, error) {
			result, err := buildDesignPlanToolResult(out)
			if err != nil {
				return nil, err
			}
			if state != nil {
				state.SavePlanContent(result.DataPart.PlanContent)
			}
			return result, nil
		},
	}
}

// newImplementRequirementTool 返回 leader 侧真正挂到 MCP 上的 `delegate_to_requirement_implementer` 工具。
//
// 这个工具接收上一阶段产出的 `plan_content`，再把执行结果解成
// Team Mode 文档里的 ImplementRequirementArtifact，并把完整制品内容回给 leader。
func newImplementRequirementTool(task DemoTaskContext, state *demoWorkflowState, inv InvocationContext) a2adelegation.ToolSpec {
	return a2adelegation.ToolSpec{
		Name:        implementRequirementToolName,
		Description: "Ask the requirement implementer to execute the approved plan in the workspace and return a structured implementation artifact.",
		InputSchema: implementRequirementInputSchema(),
		BuildRequest: func(ctx context.Context, raw json.RawMessage, env a2adelegation.ToolContext) (a2adelegation.DelegationRequest, error) {
			input, err := decodeImplementRequirementToolRequest(raw)
			if err != nil {
				return a2adelegation.DelegationRequest{}, err
			}
			planContent := ""
			if state != nil {
				planContent = state.LoadPlanContent()
			}
			if planContent == "" {
				return a2adelegation.DelegationRequest{}, fmt.Errorf("team-demo: missing plan_content for implement stage")
			}
			req := ImplementRequirementRequest{
				PlanContent:   planContent,
				WorkspacePath: task.WorkspacePath,
			}
			return a2adelegation.DelegationRequest{
				RunID:                  env.RunID,
				ParentToolCallID:       env.ParentToolCallID,
				Agent:                  implementerAgentKey,
				Message:                buildImplementRequirementMessage(input.Instruction, req),
				IncludeRemoteArtifacts: true,
				Tenant:                 env.Tenant,
				StageContext: a2adelegation.DelegationStageContext{
					WorkflowRunID: inv.WorkflowRunID,
					Stage:         implementRequirementStage,
					StepID:        inv.StepID,
					Attempt:       inv.Attempt,
				},
			}, nil
		},
		BuildResult: func(ctx context.Context, out a2adelegation.DelegationResult) (any, error) {
			return buildImplementRequirementToolResult(out)
		},
	}
}

// newVerifyCodeTool 返回 leader 侧真正挂到 MCP 上的 `delegate_to_code_verifier` 工具。
//
// 这个工具只负责代码验证：host 会把上一阶段产出的 plan_content 和 workspace
// 固定拼成 VerifyCodeRequest，member 只执行编译与单测验证。
func newVerifyCodeTool(task DemoTaskContext, state *demoWorkflowState, inv InvocationContext) a2adelegation.ToolSpec {
	return a2adelegation.ToolSpec{
		Name:        verifyCodeToolName,
		Description: "Ask the code verifier to run compile and unit-test checks for the current workspace.",
		InputSchema: verifyCodeInputSchema(),
		BuildRequest: func(ctx context.Context, raw json.RawMessage, env a2adelegation.ToolContext) (a2adelegation.DelegationRequest, error) {
			input, err := decodeVerifyCodeToolRequest(raw)
			if err != nil {
				return a2adelegation.DelegationRequest{}, err
			}
			planContent := ""
			if state != nil {
				planContent = state.LoadPlanContent()
			}
			if planContent == "" {
				return a2adelegation.DelegationRequest{}, fmt.Errorf("team-demo: missing plan_content for verify stage")
			}
			req := VerifyCodeRequest{
				PlanContent:   planContent,
				WorkspacePath: task.WorkspacePath,
			}
			return a2adelegation.DelegationRequest{
				RunID:                  env.RunID,
				ParentToolCallID:       env.ParentToolCallID,
				Agent:                  verifierAgentKey,
				Message:                buildVerifyCodeMessage(input.Instruction, req),
				IncludeRemoteArtifacts: true,
				Tenant:                 env.Tenant,
				StageContext: a2adelegation.DelegationStageContext{
					WorkflowRunID: inv.WorkflowRunID,
					Stage:         verifyCodeStage,
					StepID:        inv.StepID,
					Attempt:       inv.Attempt,
				},
			}, nil
		},
		BuildResult: func(ctx context.Context, out a2adelegation.DelegationResult) (any, error) {
			return buildVerifyCodeToolResult(out)
		},
	}
}

// designPlannerServerOptions 返回 member 侧 A2A server 的核心配置。
//
// 这个阶段 agent 只做两件事：
// 1. 读取 inbound A2A 请求里的 DesignPlanRequest
// 2. 让 Claude 一次性产出 `summary + artifact`，再包装成 TextPart + DataPart artifact
func designPlannerServerOptions(jsonRPCURL string) bridgea2a.ServerOptions {
	return bridgea2a.ServerOptions{
		RunStreaming: bridgea2a.RunStreamingDisabled,
		Session:      bridgea2a.SessionByContextID("design-plan-demo"),
		PromptBuilder: bridgea2a.PromptBuilderFunc(func(ctx context.Context, req bridgea2a.InboundRequest) (string, []agentadaptor.RunOption, error) {
			typed, err := decodeInboundDesignPlanRequest(req)
			if err != nil {
				return "", nil, err
			}
			instruction := inboundStageInstruction(req.Message)
			logMemberPayload("design_plan", "request", map[string]any{
				"instruction": instruction,
				"data_part":   typed,
			})
			return buildDesignPlanPrompt(typed, instruction), []agentadaptor.RunOption{
				agentadaptor.WithJSONSchemaOutputFor[designPlanModelOutput](agentadaptor.PreferNativeOutput()),
			}, nil
		}),
		ResultBuilder: bridgea2a.ResultBuilderFunc(func(ctx context.Context, req bridgea2a.InboundRequest, result agentadaptor.RunResult) (bridgea2a.BuiltResult, error) {
			modelOutput, err := agentadaptor.DecodeStructuredOutput[designPlanModelOutput](result)
			if err != nil {
				return bridgea2a.BuiltResult{}, err
			}
			summary := buildDesignPlanSummary(modelOutput, result.Summary)
			logMemberPayload("design_plan", "artifact", map[string]any{
				"text_part": summary,
				"data_part": modelOutput.Artifact,
			})
			return bridgea2a.BuiltResult{
				StatusText:              &summary,
				ReplaceDefaultArtifacts: true,
				Artifacts: []bridgea2a.ArtifactSpec{{
					Name: designPlanArtifact,
					Parts: []bridgea2a.Part{
						{Kind: bridgea2a.PartText, Text: summary},
						{Kind: bridgea2a.PartData, Data: modelOutput.Artifact},
					},
				}},
			}, nil
		}),
		AgentCard: bridgea2a.AgentCard{
			Name:        "plan-designer-agent",
			Description: "Claude Code agent that returns a short design plan",
			Version:     "1.0.0",
			URL:         jsonRPCURL,
			Capabilities: bridgea2a.Capabilities{
				Streaming: bridgea2a.CapabilityDefault,
			},
			Skills: []bridgea2a.Skill{{
				ID:          "design-plan",
				Name:        "Design Plan",
				Description: "Analyzes a small repository and proposes a short design plan",
				Tags:        []string{"planning", "team-mode"},
			}},
		},
		TaskLifecycle: bridgea2a.TaskLifecycleOptions{
			Ephemeral: &bridgea2a.EphemeralTaskStoreOptions{
				MaxTasks: 64,
				TTL:      30 * time.Minute,
			},
		},
	}
}

// implementRequirementServerOptions 返回执行阶段 member 的 A2A server 配置。
//
// 这个阶段 agent 会真实修改工作区，并在本地 git 仓库里提交一次 commit。
func implementRequirementServerOptions(jsonRPCURL string) bridgea2a.ServerOptions {
	return bridgea2a.ServerOptions{
		RunStreaming: bridgea2a.RunStreamingDisabled,
		Session:      bridgea2a.SessionByContextID("implement-requirement-demo"),
		PromptBuilder: bridgea2a.PromptBuilderFunc(func(ctx context.Context, req bridgea2a.InboundRequest) (string, []agentadaptor.RunOption, error) {
			typed, err := decodeInboundImplementRequirementRequest(req)
			if err != nil {
				return "", nil, err
			}
			instruction := inboundStageInstruction(req.Message)
			logMemberPayload("implement_requirement", "request", map[string]any{
				"instruction": instruction,
				"data_part":   typed,
			})
			return buildImplementRequirementPrompt(typed, instruction), []agentadaptor.RunOption{
				agentadaptor.WithJSONSchemaOutputFor[implementRequirementModelOutput](agentadaptor.PreferNativeOutput()),
			}, nil
		}),
		ResultBuilder: bridgea2a.ResultBuilderFunc(func(ctx context.Context, req bridgea2a.InboundRequest, result agentadaptor.RunResult) (bridgea2a.BuiltResult, error) {
			typedReq, err := decodeInboundImplementRequirementRequest(req)
			if err != nil {
				return bridgea2a.BuiltResult{}, err
			}
			modelOutput, err := agentadaptor.DecodeStructuredOutput[implementRequirementModelOutput](result)
			if err != nil {
				return bridgea2a.BuiltResult{}, err
			}
			if head, headErr := repoHeadCommit(typedReq.WorkspacePath); headErr == nil && strings.TrimSpace(head) != "" {
				modelOutput.Artifact.Commit = strings.TrimSpace(head)
			}
			summary := buildImplementRequirementSummary(modelOutput, result.Summary)
			logMemberPayload("implement_requirement", "artifact", map[string]any{
				"text_part": summary,
				"data_part": modelOutput.Artifact,
			})
			return bridgea2a.BuiltResult{
				StatusText:              &summary,
				ReplaceDefaultArtifacts: true,
				Artifacts: []bridgea2a.ArtifactSpec{{
					Name: implementRequirementArtifactName,
					Parts: []bridgea2a.Part{
						{Kind: bridgea2a.PartText, Text: summary},
						{Kind: bridgea2a.PartData, Data: modelOutput.Artifact},
					},
				}},
			}, nil
		}),
		AgentCard: bridgea2a.AgentCard{
			Name:        "requirement-implementer-agent",
			Description: "Claude Code agent that executes a small implementation plan and commits the result",
			Version:     "1.0.0",
			URL:         jsonRPCURL,
			Capabilities: bridgea2a.Capabilities{
				Streaming: bridgea2a.CapabilityDefault,
			},
			Skills: []bridgea2a.Skill{{
				ID:          "implement-requirement",
				Name:        "Implement Requirement",
				Description: "Executes a small coding plan in a git workspace and returns commit metadata",
				Tags:        []string{"implementation", "team-mode"},
			}},
		},
		TaskLifecycle: bridgea2a.TaskLifecycleOptions{
			Ephemeral: &bridgea2a.EphemeralTaskStoreOptions{
				MaxTasks: 64,
				TTL:      30 * time.Minute,
			},
		},
	}
}

// verifyCodeServerOptions 返回验证阶段 member 的 A2A server 配置。
//
// 这个阶段 agent 只负责跑编译和单测，不改代码、不提交 commit。
func verifyCodeServerOptions(jsonRPCURL string) bridgea2a.ServerOptions {
	return bridgea2a.ServerOptions{
		RunStreaming: bridgea2a.RunStreamingDisabled,
		Session:      bridgea2a.SessionByContextID("verify-code-demo"),
		PromptBuilder: bridgea2a.PromptBuilderFunc(func(ctx context.Context, req bridgea2a.InboundRequest) (string, []agentadaptor.RunOption, error) {
			typed, err := decodeInboundVerifyCodeRequest(req)
			if err != nil {
				return "", nil, err
			}
			instruction := inboundStageInstruction(req.Message)
			logMemberPayload("verify_code", "request", map[string]any{
				"instruction": instruction,
				"data_part":   typed,
			})
			return buildVerifyCodePrompt(typed, instruction), []agentadaptor.RunOption{
				agentadaptor.WithJSONSchemaOutputFor[verifyCodeModelOutput](agentadaptor.PreferNativeOutput()),
			}, nil
		}),
		ResultBuilder: bridgea2a.ResultBuilderFunc(func(ctx context.Context, req bridgea2a.InboundRequest, result agentadaptor.RunResult) (bridgea2a.BuiltResult, error) {
			modelOutput, err := agentadaptor.DecodeStructuredOutput[verifyCodeModelOutput](result)
			if err != nil {
				return bridgea2a.BuiltResult{}, err
			}
			summary := buildVerifyCodeSummary(modelOutput, result.Summary)
			logMemberPayload("verify_code", "artifact", map[string]any{
				"text_part": summary,
				"data_part": modelOutput.Artifact,
			})
			return bridgea2a.BuiltResult{
				StatusText:              &summary,
				ReplaceDefaultArtifacts: true,
				Artifacts: []bridgea2a.ArtifactSpec{{
					Name: verifyCodeArtifactName,
					Parts: []bridgea2a.Part{
						{Kind: bridgea2a.PartText, Text: summary},
						{Kind: bridgea2a.PartData, Data: modelOutput.Artifact},
					},
				}},
			}, nil
		}),
		AgentCard: bridgea2a.AgentCard{
			Name:        "code-verifier-agent",
			Description: "Claude Code agent that verifies build and unit test success",
			Version:     "1.0.0",
			URL:         jsonRPCURL,
			Capabilities: bridgea2a.Capabilities{
				Streaming: bridgea2a.CapabilityDefault,
			},
			Skills: []bridgea2a.Skill{{
				ID:          "verify-code",
				Name:        "Verify Code",
				Description: "Runs compile and unit-test verification for the workspace",
				Tags:        []string{"verification", "team-mode"},
			}},
		},
		TaskLifecycle: bridgea2a.TaskLifecycleOptions{
			Ephemeral: &bridgea2a.EphemeralTaskStoreOptions{
				MaxTasks: 64,
				TTL:      30 * time.Minute,
			},
		},
	}
}

// designPlanInputSchema 返回 leader 侧 `delegate_to_plan_designer` 工具的输入 schema。
//
// 这里暴露一段文本指令；host 再把固定结构化字段补齐。
func designPlanInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"instruction": map[string]any{
				"type":        "string",
				"description": "Natural language instruction for the planning stage.",
			},
		},
		"required":             []string{"instruction"},
		"additionalProperties": false,
	}
}

// implementRequirementInputSchema 返回 leader 侧 `delegate_to_requirement_implementer` 工具的输入 schema。
func implementRequirementInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"instruction": map[string]any{
				"type":        "string",
				"description": "Natural language instruction for the implementation stage.",
			},
		},
		"required":             []string{"instruction"},
		"additionalProperties": false,
	}
}

// verifyCodeInputSchema 返回 leader 侧 `delegate_to_code_verifier` 工具的输入 schema。
func verifyCodeInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"instruction": map[string]any{
				"type":        "string",
				"description": "Natural language instruction for the code verification stage.",
			},
		},
		"required":             []string{"instruction"},
		"additionalProperties": false,
	}
}

// decodeDesignPlanToolRequest 解析 leader 调 `delegate_to_plan_designer` 工具时传入的 JSON。
func decodeDesignPlanToolRequest(raw json.RawMessage) (designPlanToolInput, error) {
	var req designPlanToolInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return designPlanToolInput{}, err
	}
	if strings.TrimSpace(req.Instruction) == "" {
		return designPlanToolInput{}, fmt.Errorf("instruction is required")
	}
	return req, nil
}

// decodeImplementRequirementToolRequest 解析 leader 调 `delegate_to_requirement_implementer` 工具时传入的 JSON。
func decodeImplementRequirementToolRequest(raw json.RawMessage) (implementRequirementToolInput, error) {
	var req implementRequirementToolInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return implementRequirementToolInput{}, err
	}
	if strings.TrimSpace(req.Instruction) == "" {
		return implementRequirementToolInput{}, fmt.Errorf("instruction is required")
	}
	return req, nil
}

// decodeVerifyCodeToolRequest 解析 leader 调 `delegate_to_code_verifier` 工具时传入的 JSON。
func decodeVerifyCodeToolRequest(raw json.RawMessage) (verifyCodeToolInput, error) {
	var req verifyCodeToolInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return verifyCodeToolInput{}, err
	}
	if strings.TrimSpace(req.Instruction) == "" {
		return verifyCodeToolInput{}, fmt.Errorf("instruction is required")
	}
	return req, nil
}

// decodeInboundDesignPlanRequest 从 member 侧收到的 A2A DataPart 中解出请求。
func decodeInboundDesignPlanRequest(req bridgea2a.InboundRequest) (DesignPlanRequest, error) {
	for _, part := range req.Message.Parts {
		if part.Kind != bridgea2a.PartData {
			continue
		}
		raw, err := json.Marshal(part.Data)
		if err != nil {
			return DesignPlanRequest{}, err
		}
		var typed DesignPlanRequest
		if err := json.Unmarshal(raw, &typed); err != nil {
			return DesignPlanRequest{}, err
		}
		return typed, nil
	}
	return DesignPlanRequest{}, fmt.Errorf("design-plan-team: inbound request is missing data part")
}

// decodeInboundImplementRequirementRequest 从 member 侧收到的 A2A DataPart 中解出请求。
func decodeInboundImplementRequirementRequest(req bridgea2a.InboundRequest) (ImplementRequirementRequest, error) {
	for _, part := range req.Message.Parts {
		if part.Kind != bridgea2a.PartData {
			continue
		}
		raw, err := json.Marshal(part.Data)
		if err != nil {
			return ImplementRequirementRequest{}, err
		}
		var typed ImplementRequirementRequest
		if err := json.Unmarshal(raw, &typed); err != nil {
			return ImplementRequirementRequest{}, err
		}
		return typed, nil
	}
	return ImplementRequirementRequest{}, fmt.Errorf("team-demo: inbound implement request is missing data part")
}

// decodeInboundVerifyCodeRequest 从 member 侧收到的 A2A DataPart 中解出请求。
func decodeInboundVerifyCodeRequest(req bridgea2a.InboundRequest) (VerifyCodeRequest, error) {
	for _, part := range req.Message.Parts {
		if part.Kind != bridgea2a.PartData {
			continue
		}
		raw, err := json.Marshal(part.Data)
		if err != nil {
			return VerifyCodeRequest{}, err
		}
		var typed VerifyCodeRequest
		if err := json.Unmarshal(raw, &typed); err != nil {
			return VerifyCodeRequest{}, err
		}
		return typed, nil
	}
	return VerifyCodeRequest{}, fmt.Errorf("team-demo: inbound verify request is missing data part")
}

// buildDesignPlanMessage 把 leader 文本指令和结构化 request 一起转成发给 member 的 A2A message。
func buildDesignPlanMessage(instruction string, req DesignPlanRequest) *clienta2a.Message {
	return &clienta2a.Message{
		Role: "user",
		Parts: []clienta2a.Part{
			{Kind: clienta2a.PartText, Text: strings.TrimSpace(instruction)},
			{Kind: clienta2a.PartData, Data: req},
		},
	}
}

// buildImplementRequirementMessage 把 leader 文本指令和结构化 request 一起转成发给执行 member 的 A2A message。
func buildImplementRequirementMessage(instruction string, req ImplementRequirementRequest) *clienta2a.Message {
	return &clienta2a.Message{
		Role: "user",
		Parts: []clienta2a.Part{
			{Kind: clienta2a.PartText, Text: strings.TrimSpace(instruction)},
			{Kind: clienta2a.PartData, Data: req},
		},
	}
}

// buildVerifyCodeMessage 把 leader 文本指令和结构化 request 一起转成发给验证 member 的 A2A message。
func buildVerifyCodeMessage(instruction string, req VerifyCodeRequest) *clienta2a.Message {
	return &clienta2a.Message{
		Role: "user",
		Parts: []clienta2a.Part{
			{Kind: clienta2a.PartText, Text: strings.TrimSpace(instruction)},
			{Kind: clienta2a.PartData, Data: req},
		},
	}
}

// buildDesignPlanToolResult 把 delegate_to_plan_designer 的完整制品内容整理成 MCP 返回值。
func buildDesignPlanToolResult(out a2adelegation.DelegationResult) (DesignPlanToolResult, error) {
	for _, artifact := range out.RemoteArtifacts {
		if artifact.Name != designPlanArtifact {
			continue
		}
		result := DesignPlanToolResult{ArtifactName: artifact.Name}
		for _, part := range artifact.Parts {
			switch part.Kind {
			case clienta2a.PartText:
				if strings.TrimSpace(result.TextPart) == "" {
					result.TextPart = strings.TrimSpace(part.Text)
				}
			case clienta2a.PartData:
				raw, err := json.Marshal(part.Data)
				if err != nil {
					return DesignPlanToolResult{}, err
				}
				if err := json.Unmarshal(raw, &result.DataPart); err != nil {
					return DesignPlanToolResult{}, err
				}
			}
		}
		if strings.TrimSpace(result.TextPart) == "" {
			return DesignPlanToolResult{}, fmt.Errorf("design-plan-team: artifact %q is missing text part", artifact.Name)
		}
		if strings.TrimSpace(result.DataPart.PlanContent) == "" {
			return DesignPlanToolResult{}, fmt.Errorf("design-plan-team: artifact %q is missing data part", artifact.Name)
		}
		return result, nil
	}
	return DesignPlanToolResult{}, fmt.Errorf("design-plan-team: artifact %q not found in delegation result", designPlanArtifact)
}

// buildImplementRequirementToolResult 把 delegate_to_requirement_implementer 的完整制品内容整理成 MCP 返回值。
func buildImplementRequirementToolResult(out a2adelegation.DelegationResult) (ImplementRequirementToolResult, error) {
	for _, artifact := range out.RemoteArtifacts {
		if artifact.Name != implementRequirementArtifactName {
			continue
		}
		result := ImplementRequirementToolResult{ArtifactName: artifact.Name}
		for _, part := range artifact.Parts {
			switch part.Kind {
			case clienta2a.PartText:
				if strings.TrimSpace(result.TextPart) == "" {
					result.TextPart = strings.TrimSpace(part.Text)
				}
			case clienta2a.PartData:
				raw, err := json.Marshal(part.Data)
				if err != nil {
					return ImplementRequirementToolResult{}, err
				}
				if err := json.Unmarshal(raw, &result.DataPart); err != nil {
					return ImplementRequirementToolResult{}, err
				}
			}
		}
		if strings.TrimSpace(result.TextPart) == "" {
			return ImplementRequirementToolResult{}, fmt.Errorf("team-demo: artifact %q is missing text part", artifact.Name)
		}
		if strings.TrimSpace(result.DataPart.Commit) == "" {
			return ImplementRequirementToolResult{}, fmt.Errorf("team-demo: artifact %q is missing data part", artifact.Name)
		}
		return result, nil
	}
	return ImplementRequirementToolResult{}, fmt.Errorf("team-demo: artifact %q not found in delegation result", implementRequirementArtifactName)
}

// buildVerifyCodeToolResult 把 delegate_to_code_verifier 的完整制品内容整理成 MCP 返回值。
func buildVerifyCodeToolResult(out a2adelegation.DelegationResult) (VerifyCodeToolResult, error) {
	for _, artifact := range out.RemoteArtifacts {
		if artifact.Name != verifyCodeArtifactName {
			continue
		}
		result := VerifyCodeToolResult{ArtifactName: artifact.Name}
		for _, part := range artifact.Parts {
			switch part.Kind {
			case clienta2a.PartText:
				if strings.TrimSpace(result.TextPart) == "" {
					result.TextPart = strings.TrimSpace(part.Text)
				}
			case clienta2a.PartData:
				raw, err := json.Marshal(part.Data)
				if err != nil {
					return VerifyCodeToolResult{}, err
				}
				if err := json.Unmarshal(raw, &result.DataPart); err != nil {
					return VerifyCodeToolResult{}, err
				}
			}
		}
		if strings.TrimSpace(result.TextPart) == "" {
			return VerifyCodeToolResult{}, fmt.Errorf("team-demo: artifact %q is missing text part", artifact.Name)
		}
		if strings.TrimSpace(result.DataPart.State) == "" {
			return VerifyCodeToolResult{}, fmt.Errorf("team-demo: artifact %q is missing data part", artifact.Name)
		}
		return result, nil
	}
	return VerifyCodeToolResult{}, fmt.Errorf("team-demo: artifact %q not found in delegation result", verifyCodeArtifactName)
}

// buildDesignPlanPrompt 生成真正给 Claude member 执行的 prompt。
//
// 这里会明确要求模型同时产出两部分：
// 1. summary：给 UI 展示的简短摘要
// 2. artifact.plan_content：对外正式交付的 DesignPlanArtifact
func buildDesignPlanPrompt(req DesignPlanRequest, stageInstruction string) string {
	instructionBlock := ""
	if strings.TrimSpace(stageInstruction) != "" {
		instructionBlock = fmt.Sprintf("leader 传下来的文本指令：\n%s\n\n", strings.TrimSpace(stageInstruction))
	}
	return fmt.Sprintf(`你是一个“Design Plan”阶段 agent，只做方案，不改代码。

请根据以下输入，为一个仓库生成简短设计方案：

%s

- requirement_title: %s
- requirement_content: %s
- requirement_attachment_path: %s
- prototype_path: %s
- workspace_path: %s

要求：
1. 先快速理解仓库结构
2. 输出一个很小的方案，重点是应该改哪些文件、改哪些点
3. summary 要简短，给 UI 直接展示
4. artifact.plan_content 用 Markdown 输出，并作为正式的 DesignPlanArtifact
5. 如果提到验证命令，优先使用不会在工作区留下构建产物的命令（例如 "go build ." 或 "go test ./..."）
6. 不要真的修改代码`, instructionBlock, req.RequirementTitle, req.RequirementContent, req.RequirementAttachmentPath, req.PrototypePath, req.WorkspacePath)
}

// buildImplementRequirementPrompt 生成真正给 Claude member 执行实现阶段的 prompt。
func buildImplementRequirementPrompt(req ImplementRequirementRequest, stageInstruction string) string {
	instructionBlock := ""
	if strings.TrimSpace(stageInstruction) != "" {
		instructionBlock = fmt.Sprintf("leader 传下来的文本指令：\n%s\n\n", strings.TrimSpace(stageInstruction))
	}
	return fmt.Sprintf(`你是一个“Implement Requirement”阶段 agent，负责真实修改代码并提交 commit。

输入：
%s
- workspace_path: %s
- plan_content:
%s

要求：
1. 严格按 plan_content 执行最小改动
2. 可以编辑文件、运行必要命令、然后提交一次 git commit
3. 至少做一次最小验证（例如 "go build ."）
4. 不要在工作区留下未跟踪的构建产物；如果你生成了二进制或临时文件，提交前先清理
5. 返回时同时提供：
   - summary：一句简短实现摘要
   - artifact.mr_title_draft
   - artifact.mr_body_draft
   - artifact.commit
   - artifact.mr_labels（可选）
6. commit 字段必须是执行完成后的 HEAD commit SHA`, instructionBlock, req.WorkspacePath, req.PlanContent)
}

// buildVerifyCodePrompt 生成真正给 Claude member 执行验证阶段的 prompt。
func buildVerifyCodePrompt(req VerifyCodeRequest, stageInstruction string) string {
	instructionBlock := ""
	if strings.TrimSpace(stageInstruction) != "" {
		instructionBlock = fmt.Sprintf("leader 传下来的文本指令：\n%s\n\n", strings.TrimSpace(stageInstruction))
	}
	return fmt.Sprintf(`你是一个“Verify Code”阶段 agent，只做验证，不改代码、不提交 commit。

输入：
%s
- workspace_path: %s
- plan_content:
%s

要求：
1. 只做两项验证：
   - 对当前这个单二进制 Go demo，使用 "go build -o /tmp/team-mode-verify-bin ." 做编译验证，避免在 workspace 生成二进制
   - 执行 "go test ./..."
2. 两个命令都成功时，artifact.state 必须返回 %s。
3. 只要任意一个命令失败，artifact.state 必须返回 %s，并在 artifact.description 里说明哪个命令失败以及关键错误。
4. artifact.description 要给出人类可读的验证结论。
5. artifact.report_paths 默认留空，除非你真的生成了需要保留的验证报告文件。
6. 如果验证过程仍然产生了二进制或其他构建产物，返回前先清理，避免工作区留下未跟踪文件。
7. 不要修改代码，不要新增 commit，不要创建 MR。`,
		instructionBlock, req.WorkspacePath, req.PlanContent, VerifyCodeStatePassed, VerifyCodeStateFailed)
}

// inboundStageInstruction 从 inbound request 的 TextPart 里取回 leader 文本指令。
func inboundStageInstruction(msg bridgea2a.Message) string {
	texts := make([]string, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		if part.Kind != bridgea2a.PartText {
			continue
		}
		text := strings.TrimSpace(part.Text)
		if text == "" {
			continue
		}
		texts = append(texts, text)
	}
	return strings.TrimSpace(strings.Join(texts, "\n\n"))
}

// logMemberPayload 打印 member 侧收到的 request 或产出的 artifact。
func logMemberPayload(stage, kind string, payload any) {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[member %s %s] %v\n", stage, kind, payload)
		return
	}
	fmt.Fprintf(os.Stderr, "\n[member %s %s]\n%s\n", stage, kind, string(raw))
}

// buildVerifyCodeSummary 生成验证阶段 artifact 的 TextPart 摘要。
func buildVerifyCodeSummary(modelOutput verifyCodeModelOutput, fallback string) string {
	if strings.TrimSpace(modelOutput.Summary) != "" {
		return strings.TrimSpace(modelOutput.Summary)
	}
	if strings.TrimSpace(modelOutput.Artifact.Description) != "" {
		return strings.TrimSpace(modelOutput.Artifact.Description)
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	switch strings.TrimSpace(modelOutput.Artifact.State) {
	case VerifyCodeStatePassed:
		return "代码验证通过。"
	case VerifyCodeStateFailed:
		return "代码验证失败。"
	case VerifyCodeStateSkipped:
		return "代码验证已跳过。"
	default:
		return "已完成 verify code。"
	}
}

// buildDesignPlanSummary 生成最终 artifact 的 TextPart 摘要。
//
// 正常情况下，这里优先使用大模型产出的 summary。
// 只有模型没给 summary 时，才回退到 host 侧兜底文案。
func buildDesignPlanSummary(modelOutput designPlanModelOutput, fallback string) string {
	if strings.TrimSpace(modelOutput.Summary) != "" {
		return strings.TrimSpace(modelOutput.Summary)
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	if strings.TrimSpace(modelOutput.Artifact.PlanContent) != "" {
		return "已生成设计方案。"
	}
	return "已完成 design plan。"
}

// buildImplementRequirementSummary 生成实现阶段 artifact 的 TextPart 摘要。
func buildImplementRequirementSummary(modelOutput implementRequirementModelOutput, fallback string) string {
	if strings.TrimSpace(modelOutput.Summary) != "" {
		return strings.TrimSpace(modelOutput.Summary)
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	if strings.TrimSpace(modelOutput.Artifact.Commit) != "" {
		return "已完成实现并生成提交。"
	}
	return "已完成 implement requirement。"
}

// repoHeadCommit 返回当前工作区真实的 HEAD commit。
//
// 对执行阶段来说，这比完全相信大模型返回的 commit 更稳，因为它直接读本地 git 状态。
func repoHeadCommit(workspacePath string) (string, error) {
	return commandOutput(workspacePath, "git", "rev-parse", "HEAD")
}
