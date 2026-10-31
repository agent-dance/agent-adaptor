package pi

import (
	"encoding/json"
	"net/http"

	"github.com/agent-dance/agent-adaptor/driver"
)

const reviewPlanTool = "adaptor_review_plan"
const askUserTool = "adaptor_ask_user"

// Explicit policy enables interception. Zero policy preserves native behavior,
// just as Claude's observational print transport does. Once enabled, all three
// decision classes use the single core policy and responder implementation.
func wantsInteractions(p driver.HumanDecisionPolicy) bool {
	return p.Permission == driver.HumanDecisionAsk || p.Permission == driver.HumanDecisionAutoReject ||
		p.PlanReview != driver.HumanDecisionUnset || p.Question != driver.QuestionUnset
}

type extensionDecision struct {
	ToolCallID string         `json:"toolCallId"`
	ToolName   string         `json:"toolName"`
	Input      map[string]any `json:"input"`
}

func (b *mcpBridge) serveDecision(w http.ResponseWriter, r *http.Request) {
	if b.decisions == nil {
		http.NotFound(w, r)
		return
	}
	var call extensionDecision
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&call); err != nil || call.ToolCallID == "" || call.ToolName == "" || call.Input == nil {
		http.Error(w, "invalid Pi tool decision", http.StatusBadRequest)
		return
	}
	req := driver.DecisionRequest{RunID: b.request.RunID, Kind: driver.HumanDecisionPermission,
		Source: "pi.tool_call", ToolCallID: call.ToolCallID, Prompt: "Allow Pi tool " + call.ToolName + "?",
		Payload: map[string]any{"tool_name": call.ToolName, "input": call.Input}}
	switch call.ToolName {
	case reviewPlanTool:
		req.Kind, req.Source = driver.HumanDecisionPlanReview, "pi.review_plan"
		req.Prompt, _ = call.Input["plan"].(string)
	case askUserTool:
		req.Kind, req.Source = driver.HumanDecisionQuestion, "pi.ask_user"
		req.Prompt, _ = call.Input["question"].(string)
		if options, ok := call.Input["options"].([]any); ok {
			for _, option := range options {
				if label, ok := option.(string); ok {
					req.Choices = append(req.Choices, driver.DecisionChoice{Key: label, Label: label})
				}
			}
		}
	}
	if req.Prompt == "" {
		http.Error(w, "decision prompt is empty", http.StatusBadRequest)
		return
	}
	// The sink owns defaults, timeout, retries, fallback, typed Events and
	// exactly-once responses. A sink error ends the run without replaying the
	// prompt; a rejected response with nil error means core chose Continue.
	response, err := b.decisions.RequestDecision(r.Context(), req)
	if err != nil {
		b.abort(err)
		http.Error(w, "approval ended the invocation", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"result": response.Result, "choice": response.Choice, "answer": response.Answer, "text": response.Text})
}

// This is provider code, loaded via Pi's explicit --extension mechanism.
// Permission is triggered by Pi's real pre-execution tool_call event. Plan and
// question tools are explicitly registered with Pi; no transcript-text matching
// or synthetic inference of user intent creates an approval request.
const interactionExtension = `
function installInteractions(pi) {
  if (toolPolicy) {
    const allowed = name => !(toolPolicy.Deny || []).includes(name) && (!(toolPolicy.Allow || []).length || toolPolicy.Allow.includes(name));
    pi.on("session_start", () => { pi.setActiveTools(pi.getActiveTools().filter(allowed)); });
    pi.on("tool_call", event => allowed(event.toolName) ? undefined : {block:true,reason:"Subagent tool policy denies this tool"});
  }
  if (!interaction.enabled) return;
  async function decide(toolCallId, toolName, input, signal) {
    const response = await fetch(process.env[urlKey] + "/decision", {
      method: "POST", signal,
      headers: { "Authorization": "Bearer " + process.env[tokenKey], "Content-Type": "application/json" },
      body: JSON.stringify({toolCallId, toolName, input})
    });
    if (!response.ok) throw new Error("Host approval unavailable: " + response.status);
    return response.json();
  }
  if (interaction.permission) pi.on("tool_call", async (event, ctx) => {
    if (event.toolName === "adaptor_review_plan" || event.toolName === "adaptor_ask_user") return;
    const result = await decide(event.toolCallId, event.toolName, event.input, ctx.signal);
    if (result.result !== "approved") return {block: true, reason: result.text || "Host denied tool execution"};
  });
  pi.registerTool({
    name: "adaptor_review_plan", label: "Review plan",
    description: "Submit an implementation plan for host approval before executing it. If rejected, do not execute that plan.",
    parameters: {type: "object", properties: {plan: {type: "string", minLength: 1}}, required: ["plan"], additionalProperties: false},
    async execute(id, args, signal) {
      const result = await decide(id, "adaptor_review_plan", args, signal);
      if (result.result !== "approved") throw new Error(result.text || "Host rejected the plan");
      return {content: [{type: "text", text: "Host approved the plan"}], details: result};
    }
  });
  pi.registerTool({
    name: "adaptor_ask_user", label: "Ask user",
    description: "Ask the host a clarification question. Supply optional answer choices; free-form answers are also supported.",
    parameters: {type: "object", properties: {question: {type: "string", minLength: 1}, options: {type: "array", items: {type: "string"}}}, required: ["question"], additionalProperties: false},
    async execute(id, args, signal) {
      const result = await decide(id, "adaptor_ask_user", args, signal);
      if (result.result !== "answered") throw new Error(result.text || "Host declined to answer");
      return {content: [{type: "text", text: JSON.stringify({choice: result.choice, answer: result.answer, text: result.text})}], details: result};
    }
  });
}
`
