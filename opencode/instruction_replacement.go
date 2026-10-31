package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func replacementInstructions(req driver.Request) bool {
	return req.Instructions != nil && req.Instructions.Mode == driver.InstructionModeReplace
}

type nativeAgentState struct {
	defaultAgent string
	agents       map[string]map[string]any
}

// Read only the official resolved config/agent endpoints. Keeping complete
// agent objects allows us to detect changes to unknown future native fields,
// as well as permissions, model, tools, mode and options, before prompt delivery.
func (c *serverClient) nativeAgents(ctx context.Context) (nativeAgentState, error) {
	state := nativeAgentState{agents: map[string]map[string]any{}}
	raw, err := c.call(ctx, "GET", "/config", nil)
	if err != nil {
		return state, err
	}
	var config struct {
		DefaultAgent string `json:"default_agent"`
	}
	if err := decodeNativeObject(raw, &config); err != nil {
		return state, fmt.Errorf("opencode: malformed resolved config: %w", err)
	}
	state.defaultAgent = config.DefaultAgent
	raw, err = c.call(ctx, "GET", "/agent", nil)
	if err != nil {
		return state, err
	}
	var agents []map[string]any
	if err := decodeNativeObject(raw, &agents); err != nil {
		return state, fmt.Errorf("opencode: malformed resolved agents: %w", err)
	}
	for _, agent := range agents {
		name, _ := agent["name"].(string)
		mode, _ := agent["mode"].(string)
		if name == "" || (mode != "primary" && mode != "subagent" && mode != "all") || state.agents[name] != nil {
			return state, errors.New("opencode: invalid or duplicate resolved agent")
		}
		if rules, ok := agent["permission"].([]any); ok {
			agent["permission"] = canonicalPermissions(rules)
		}
		state.agents[name] = agent
	}
	if len(state.agents) == 0 {
		return state, errors.New("opencode: no resolved native agents")
	}
	return state, nil
}

// OpenCode discovers native skill directories concurrently. Their adjacent
// allow rules can arrive in different orders across processes. Rules with the
// same action commute; never sort across action boundaries, since native
// permissions use last-match precedence. Unknown rule shapes remain exact.
func canonicalPermissions(rules []any) []any {
	action := func(value any) string {
		rule, ok := value.(map[string]any)
		if !ok || len(rule) != 3 {
			return ""
		}
		if _, ok := rule["permission"].(string); !ok {
			return ""
		}
		if _, ok := rule["pattern"].(string); !ok {
			return ""
		}
		s, _ := rule["action"].(string)
		if s != "allow" && s != "ask" && s != "deny" {
			return ""
		}
		return s
	}
	out := append([]any(nil), rules...)
	for start := 0; start < len(out); {
		kind := action(out[start])
		end := start + 1
		for kind != "" && end < len(out) && action(out[end]) == kind {
			end++
		}
		sort.Slice(out[start:end], func(i, j int) bool {
			left, _ := json.Marshal(out[start+i])
			right, _ := json.Marshal(out[start+j])
			return bytes.Compare(left, right) < 0
		})
		start = end
	}
	return out
}

func decodeNativeObject(raw []byte, target any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("unexpected null")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing native payload")
	}
	return nil
}

type instructionReplacement struct {
	env         []driver.EnvBinding
	expected    nativeAgentState
	permissions bool
}

func (c *serverClient) nativeAgentOverlay(ctx context.Context, selected string, replacement *string, gatePermissions bool, internalOutputGlob string, env []driver.EnvBinding) (instructionReplacement, error) {
	plan := instructionReplacement{permissions: gatePermissions}
	state, err := c.nativeAgents(ctx)
	if err != nil {
		return plan, err
	}
	if selected != "" && state.agents[selected] == nil {
		return plan, fmt.Errorf("opencode: native agent %q is unavailable", selected)
	}
	config := map[string]any{}
	var preservedMCP json.RawMessage
	preservedAgents := map[string]json.RawMessage{}
	if raw, _ := driverutil.ResolvedEnvValue(env, "OPENCODE_CONFIG_CONTENT"); raw != "" {
		if err := decodeNativeObject([]byte(raw), &config); err != nil {
			return plan, fmt.Errorf("opencode: malformed invocation config: %w", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return plan, err
		}
		preservedMCP = fields["mcp"]
		if fields["agent"] != nil {
			if err := json.Unmarshal(fields["agent"], &preservedAgents); err != nil {
				return plan, err
			}
		}
	}
	overlay, err := objectField(config, "agent")
	if err != nil {
		return plan, err
	}
	// An empty string would restore OpenCode's built-in preamble (its native
	// branch tests truthiness). A newline represents an intentionally empty base.
	content := ""
	if replacement != nil {
		content = *replacement
	}
	if content == "" {
		content = "\n"
	}
	prompt, _ := json.Marshal(content)
	// The native config loader expands {env:...}/{file:...} in raw JSON.
	// Escape only our literal prompt; preserve user-provided config templates.
	prompt = bytes.ReplaceAll(prompt, []byte("{"), []byte(`\u007b`))
	count := 0
	for name, agent := range state.agents {
		// With no explicit selection, keep native default resolution untouched.
		// Apply to visible primary-capable agents, including plan/build switches;
		// hidden title/compaction helpers and subagent-only prompts stay intact.
		hidden, _ := agent["hidden"].(bool)
		replace := replacement != nil && ((selected != "" && selected == name) || (selected == "" && agent["mode"] != "subagent" && !hidden))
		if !replace && !gatePermissions {
			continue
		}
		entry, err := objectField(overlay, name)
		if err != nil {
			return plan, err
		}
		literal := map[string]json.RawMessage{}
		if raw := preservedAgents[name]; raw != nil {
			if err := json.Unmarshal(raw, &literal); err != nil {
				return plan, err
			}
		}
		if replace {
			entry["prompt"] = json.RawMessage(prompt)
			literal["prompt"] = prompt
			agent["prompt"] = content
			count++
		}
		if gatePermissions {
			permission, rules, err := gateAgentPermissions(agent["permission"], internalOutputGlob)
			if err != nil {
				return plan, err
			}
			literal["permission"] = permission
			agent["permission"] = rules
		}
		preservedAgents[name], err = json.Marshal(literal)
		if err != nil {
			return plan, err
		}
	}
	if replacement != nil && count == 0 {
		return plan, errors.New("opencode: no primary native agent accepts replacement instructions")
	}
	// Retain Driver-escaped MCP literals across the discovery restart.
	if preservedMCP != nil {
		config["mcp"] = preservedMCP
	}
	config["agent"] = preservedAgents
	raw, err := json.Marshal(config)
	if err != nil {
		return plan, err
	}
	plan.env = skillruntime.WithBinding(env, "OPENCODE_CONFIG_CONTENT", string(raw))
	if gatePermissions {
		if err := normalizeAgentPermissions(&state); err != nil {
			return plan, err
		}
	}
	plan.expected = state
	return plan, nil
}

func (p instructionReplacement) verify(ctx context.Context, client *serverClient) error {
	actual, err := client.nativeAgents(ctx)
	if err != nil {
		return err
	}
	if p.permissions {
		if err := normalizeAgentPermissions(&actual); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(actual, p.expected) {
		// Do not print resolved settings: they can contain credentials or private
		// instructions. A mismatch is a startup failure, never a warning/fallback.
		return fmt.Errorf("opencode: native agent overlay was not applied exactly or resolved settings changed before prompt delivery (%s)", nativeAgentDifferences(p.expected, actual))
	}
	return nil
}

// Report field identities only, never values from native configuration.
func nativeAgentDifferences(expected, actual nativeAgentState) string {
	var fields []string
	if expected.defaultAgent != actual.defaultAgent {
		fields = append(fields, "default_agent")
	}
	for name, wanted := range expected.agents {
		observed := actual.agents[name]
		if observed == nil {
			fields = append(fields, "missing agent "+name)
			continue
		}
		for key, value := range wanted {
			if other, ok := observed[key]; !ok || !reflect.DeepEqual(value, other) {
				fields = append(fields, name+"."+key)
			}
		}
		for key := range observed {
			if _, ok := wanted[key]; !ok {
				fields = append(fields, name+"."+key)
			}
		}
	}
	for name := range actual.agents {
		if expected.agents[name] == nil {
			fields = append(fields, "unexpected agent "+name)
		}
	}
	sort.Strings(fields)
	return strings.Join(fields, ", ")
}
