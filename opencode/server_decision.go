package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"

	"github.com/agent-dance/agent-adaptor/driver"
)

type permissionRule struct {
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     string `json:"action"`
}

// The native session PATCH appends rules; [] cannot restore an earlier policy.
// Start a new last-match segment containing the resolved agent and the original
// session baseline. A wildcard reset removes only previous SDK segments, while
// every native deny and its precedence is replayed in the current segment.
func (c *serverClient) configureSessionPermissions(ctx context.Context, agent, sessionID string, baseline, current []permissionRule, gated bool) error {
	state, err := c.nativeAgents(ctx)
	if err != nil {
		return err
	}
	if agent == "" {
		agent = state.defaultAgent
		if agent == "" {
			agent = "build"
		}
	}
	entry := state.agents[agent]
	if entry == nil {
		return fmt.Errorf("opencode: native agent %q is unavailable", agent)
	}
	rules, err := permissionRules(entry["permission"])
	if err != nil {
		return err
	}
	rules = append([]permissionRule{{Permission: "*", Pattern: "*", Action: "ask"}}, rules...)
	rules = append(rules, baseline...)
	if gated {
		for i := range rules {
			if rules[i].Action == "allow" {
				rules[i].Action = "ask"
			}
		}
	}
	wanted := effectivePermissionRules(rules)
	if reflect.DeepEqual(effectivePermissionRules(current), wanted) {
		return nil
	}
	raw, err := c.call(ctx, "PATCH", "/session/"+url.PathEscape(sessionID), map[string]any{"permission": wanted})
	if err != nil {
		return err
	}
	var updated struct {
		ID         string          `json:"id"`
		Permission json.RawMessage `json:"permission"`
	}
	if err := decodeNativeObject(raw, &updated); err != nil || updated.ID != sessionID || len(updated.Permission) == 0 {
		return errors.New("opencode: session permission update omitted its native session state")
	}
	actual, err := permissionRules(updated.Permission)
	if err != nil || !reflect.DeepEqual(effectivePermissionRules(actual), wanted) {
		return errors.New("opencode: session permission update did not deliver the resolved policy")
	}
	return nil
}

func (p *serverParser) decide(ctx context.Context, client *serverClient, req driver.Request, event serverEvent) error {
	var q struct {
		ID         string `json:"id"`
		SessionID  string `json:"sessionID"`
		Permission string `json:"permission"`
		Tool       *struct {
			MessageID string `json:"messageID"`
			CallID    string `json:"callID"`
		} `json:"tool"`
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
			Multiple bool   `json:"multiple"`
			Custom   *bool  `json:"custom"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(event.Properties, &q) != nil || q.ID == "" || q.SessionID != p.sessionID {
		return errors.New("opencode: malformed approval request")
	}
	key := event.Type + "/" + q.ID
	if p.decisions[key] {
		return errors.New("opencode: duplicate approval request")
	}
	p.decisions[key] = true
	sink, ok := p.events.Sink.(driver.DecisionCapableSink)
	if !ok {
		return errors.New("opencode: native approval requires a decision-capable sink")
	}
	var payload map[string]any
	_ = json.Unmarshal(event.Properties, &payload)
	d := driver.DecisionRequest{RunID: req.RunID, Kind: driver.HumanDecisionPermission, Source: "opencode.permission", Payload: payload}
	toolName := ""
	if q.Tool != nil {
		d.ToolCallID = q.Tool.CallID
		if p.child {
			d.ToolCallID = childIdentifier(p.sessionID, q.Tool.CallID)
		}
		for _, s := range p.parts {
			if s.part.CallID == q.Tool.CallID && s.part.MessageID == q.Tool.MessageID {
				toolName = s.part.Tool
				break
			}
		}
	}
	if event.Type == "permission.asked" {
		if q.Permission == "" {
			return errors.New("opencode: permission name is empty")
		}
		d.Prompt = "Allow OpenCode permission " + q.Permission + "?"
		answer, err := sink.RequestDecision(ctx, d)
		if err != nil {
			return err
		}
		reply := "reject"
		if answer.Result == driver.DecisionApproved {
			if q.Permission == "task" && toolName != "task" {
				return fmt.Errorf("opencode: task approval lacks an observed tool call: %w", errors.ErrUnsupported)
			}
			if toolName == "task" {
				for _, state := range p.parts {
					if state.part.CallID == q.Tool.CallID && state.part.MessageID == q.Tool.MessageID {
						if err := client.validateTaskResume(ctx, p.sessionID, state.part.State.Input, req); err != nil {
							return err
						}
					}
				}
			}
			reply = "once"
		}
		_, err = client.call(ctx, "POST", "/permission/"+url.PathEscape(q.ID)+"/reply", map[string]any{"reply": reply, "message": answer.Text})
		return err
	}
	if len(q.Questions) == 0 {
		return errors.New("opencode: empty question request")
	}
	answers := make([][]string, 0, len(q.Questions))
	for index, question := range q.Questions {
		if question.Question == "" {
			return errors.New("opencode: empty question")
		}
		d.Kind, d.Source, d.Prompt = driver.HumanDecisionQuestion, "opencode.question", question.Question
		d.Payload = map[string]any{"request": payload, "question_index": index}
		d.Choices = nil
		for _, option := range question.Options {
			d.Choices = append(d.Choices, driver.DecisionChoice{Key: option.Label, Label: option.Label, Description: option.Description})
		}
		// The native plan_exit tool's question is the official execution gate.
		// Identify it by the correlated tool call, never by question wording.
		if toolName == "plan_exit" {
			d.Kind, d.Source = driver.HumanDecisionPlanReview, "opencode.plan_exit"
		}
		answer, err := sink.RequestDecision(ctx, d)
		if err != nil {
			return err
		}
		if (d.Kind == driver.HumanDecisionPlanReview && answer.Result != driver.DecisionApproved) || (d.Kind == driver.HumanDecisionQuestion && answer.Result != driver.DecisionAnswered) {
			_, err = client.call(ctx, "POST", "/question/"+url.PathEscape(q.ID)+"/reject", nil)
			return err
		}
		value := answer.Text
		if answer.Choice != "" {
			value = answer.Choice
		}
		if d.Kind == driver.HumanDecisionPlanReview {
			value = "Yes"
		}
		if value == "" {
			return fmt.Errorf("opencode: question %d has no answer", index)
		}
		// A single selection is a valid subset even when OpenCode allows many.
		// Do not split free-form text on commas or guess structured selections.
		answers = append(answers, []string{value})
	}
	_, err := client.call(ctx, "POST", "/question/"+url.PathEscape(q.ID)+"/reply", map[string]any{"answers": answers})
	return err
}

func (c *serverClient) validateTaskResume(ctx context.Context, parentID string, input map[string]any, req driver.Request) error {
	value, exists := input["task_id"]
	if !exists {
		return nil
	}
	id, ok := value.(string)
	if !ok {
		return errors.New("opencode: malformed task resume identifier")
	}
	if id == "" {
		return nil
	}
	if err := c.verifyChild(ctx, id, parentID); err != nil {
		return err
	}
	if req.Policy.HumanDecision.Permission != driver.HumanDecisionAsk && req.Policy.HumanDecision.Permission != driver.HumanDecisionAutoReject {
		return nil
	}
	raw, err := c.call(ctx, "GET", "/session/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	var info struct {
		Permission []permissionRule `json:"permission"`
	}
	if err := decodeNativeObject(raw, &info); err != nil {
		return err
	}
	for _, rule := range info.Permission {
		if rule.Action == "allow" {
			return fmt.Errorf("opencode: resumed child has session-specific allow rules that can bypass invocation approvals: %w", errors.ErrUnsupported)
		}
	}
	return nil
}
