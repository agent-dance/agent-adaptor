package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/tool"
)

type alignmentDiagnosticResultDriver struct {
	configuredDriver
	response driver.Response
}

// Share the actual live callback with the offline diagnostic control. The
// total counter observes entry only; exact VERIFY remains the original oracle.
func alignmentCatalogEchoDefinition(totalEcho, actualEcho *atomic.Int32) tool.Definition {
	return tool.Define("alignment_echo", "Return the supplied text for the acceptance check.", func(_ context.Context, input struct {
		Text string `json:"text"`
	}) (string, error) {
		totalEcho.Add(1)
		if input.Text == "VERIFY" {
			actualEcho.Add(1)
		}
		return input.Text, nil
	}, tool.ReadOnly(), tool.Revision("alignment-t15/v1"))
}

func alignmentCatalogCallbackDiagnostic(total, exactVerify int) map[string]any {
	return map[string]any{"stage": "catalog", "kind": "catalog_callback", "total": total, "exact_verify": exactVerify}
}

func (d alignmentDiagnosticResultDriver) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return d.response, nil
}

func TestAlignmentLiveDiagnosticSafeProjection(t *testing.T) {
	const secret = "PRIVATE_RANDOM_MARKER"
	const session = "PRIVATE_SESSION_ID"
	call := `{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"tool_use","id":"PRIVATE_CALL_ID","name":"DeferExecuteTool","input":{"toolName":"mcp__agent-adaptor-tools__alignment_echo","params":{"text":"PRIVATE_RANDOM_MARKER","PRIVATE_KEY":"PRIVATE_VALUE"},"PRIVATE_EXTRA":true}}]}}`
	result := `{"type":"user","parent_tool_use_id":"PRIVATE_CALL_ID","message":{"content":[{"type":"tool_result","tool_use_id":"PRIVATE_CALL_ID","content":"PRIVATE_RESULT","is_error":false}]}}`
	unknown := `{"type":"assistant","parent_tool_use_id":"PRIVATE_PARENT","message":{"content":[{"type":"tool_use","id":"PRIVATE_OTHER_ID","name":"PRIVATE_TOOL_NAME","input":{"phase":"PRIVATE_PHASE"}}]}}`
	home := t.TempDir()
	d := alignmentDiagnosticResultDriver{configuredDriver: Driver(Config{CommonConfig: CommonConfig{Command: filepath.Join(home, "no-provider"), Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CODEBUDDY_CONFIG_DIR", Value: home}}}}).(configuredDriver), response: driver.Response{Output: "prefix " + secret + " suffix", RawStreams: &driver.RawStreams{Stdout: strings.Join([]string{call, result, unknown}, "\n"), Stderr: "PRIVATE_STDERR", Terminal: &driver.TerminalPayload{Event: "result", JSON: json.RawMessage(`{"type":"result","subtype":"success","session_id":"PRIVATE_SESSION_ID","is_error":false}`)}}}}
	a := adaptor.New(d)
	defer a.Close(context.Background())
	r, err := a.Run(context.Background(), "synthetic diagnostic")
	if err != nil {
		t.Fatal("synthetic diagnostic result unavailable")
	}
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"success", nil, "none"},
		{"plain_error", errors.New("PRIVATE_ERROR"), "other"},
		{"classified_error", &adaptor.RunError{Reason: adaptor.ReasonAgentError, Message: "PRIVATE_ERROR", Result: r}, "agent_error"},
		{"unknown_reason", &adaptor.RunError{Reason: "PRIVATE_REASON", Message: "PRIVATE_ERROR", Result: r}, "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := r
			if tc.err != nil {
				input = nil
			}
			facts := alignmentLiveDiagnosticFacts("cold_B", input, nil, secret, session, 1, 0, tc.err)
			first := facts[0]
			if first["run_error_code"] != tc.code || first["callback_count"] != 1 || first["wrong_phase_count"] != 0 || first["result_present"] != (input != nil) {
				t.Fatal("safe error/callback predicate classification changed")
			}
			if tc.name != "plain_error" {
				if first["text_exact_after_trim"] != false || first["text_contains_marker"] != true || first["terminal_check"] != "" || first["terminal_present"] != true || len(facts) != 4 {
					t.Fatal("nonexact text or protocol envelope predicate lost")
				}
				if facts[1]["tool_class"] != "DeferExecuteTool" || facts[1]["target_tool_class"] != "hosted_echo" || facts[1]["params_object"] != true || facts[1]["unknown_params_keys"] != 1 || facts[1]["unknown_input_keys"] != 1 || facts[2]["call_index"] != facts[1]["call_index"] || facts[2]["error_true"] != false || facts[3]["tool_class"] != "other" || facts[3]["parent_scope"] != "foreign" {
					t.Fatal("formal one-level shape or safe ID correlation changed")
				}
			}
			raw, err := json.Marshal(facts)
			if err != nil || strings.Contains(string(raw), "PRIVATE") {
				t.Fatal("diagnostic copied private name/key/value/body/error")
			}
		})
	}
	if got := alignmentLiveDiagnosticFacts("PRIVATE_STAGE", nil, nil, "", "", 0, 0, nil)[0]["stage"]; got != "other" {
		t.Fatal("unknown diagnostic label leaked")
	}
	for _, phase := range []capability.Phase{capability.Started, capability.Completed, capability.Failed, capability.Interrupted} {
		facts := alignmentLiveDiagnosticFacts("catalog", nil, []adaptor.Event{adaptor.CapabilityInvocation{Invocation: capability.Invocation{Ref: capability.Ref{Kind: capability.MCP}, Phase: phase}}}, "", "", 0, 0, nil)
		key := map[capability.Phase]string{capability.Started: "mcp_started", capability.Completed: "mcp_completed", capability.Failed: "mcp_failed", capability.Interrupted: "mcp_other_terminal"}[phase]
		if facts[0][key] != 1 {
			t.Fatal("capability phase count lost")
		}
	}
}

func alignmentLiveDiagnosticToolClass(name string) string {
	switch name {
	case "Skill", "Task", "Agent", "TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "ToolSearch", "DeferExecuteTool":
		return name
	case "mcp__agent-adaptor-tools__alignment_echo":
		return "hosted_echo"
	case "mcp__agent-adaptor-tools__alignment_history_probe":
		return "hosted_history_probe"
	default:
		return "other"
	}
}
func alignmentLiveDiagnosticKeys(input map[string]any) ([]string, int) {
	keys := []string{}
	unknown := 0
	for key := range input {
		switch key {
		case "toolName", "params", "phase", "text", "skill", "command", "subagent_type", "prompt", "description", "taskId", "status", "subject", "newTodos", "oldTodos", "query", "toolNames", "top_k":
			keys = append(keys, key)
		default:
			unknown++
		}
	}
	sort.Strings(keys)
	return keys, unknown
}
func alignmentLiveDiagnosticPhase(value any) string {
	s, ok := value.(string)
	if !ok {
		return "missing_or_malformed"
	}
	if s == "record" || s == "verify" {
		return s
	}
	return "other"
}
func alignmentLiveDiagnosticLog(t *testing.T, fields map[string]any) {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal("safe diagnostic encoding failed")
	}
	t.Logf("T28_DIAG %s", raw)
}
func alignmentLiveDiagnosticResult(t *testing.T, stage string, result *adaptor.Result, events []adaptor.Event, marker, session string, calls, wrong int, runErr error) {
	t.Helper()
	for _, fields := range alignmentLiveDiagnosticFacts(stage, result, events, marker, session, calls, wrong, runErr) {
		alignmentLiveDiagnosticLog(t, fields)
	}
}

// Project only closed protocol shapes and predicate values. Provider names,
// IDs, text, arguments, errors and credentials never become diagnostic values.
func alignmentLiveDiagnosticFacts(stage string, result *adaptor.Result, events []adaptor.Event, marker, session string, calls, wrong int, runErr error) []map[string]any {
	if stage != "cold_B" && stage != "catalog" {
		stage = "other"
	}
	var output []map[string]any
	fields := map[string]any{"stage": stage, "kind": "predicates", "go_error_present": runErr != nil, "result_present": result != nil, "callback_count": calls, "wrong_phase_count": wrong}
	fields["run_error_code"] = "none"
	var failure *adaptor.RunError
	if runErr != nil {
		fields["run_error_code"] = "other"
	}
	if errors.As(runErr, &failure) && failure != nil {
		switch failure.Reason {
		case adaptor.ReasonApprovalDenied, adaptor.ReasonApprovalTimeout, adaptor.ReasonAgentError, adaptor.ReasonCancelled, adaptor.ReasonPolicyViolation, adaptor.ReasonInfrastructure, adaptor.ReasonDeadlineExceeded, adaptor.ReasonActiveExecutionTimeout:
			fields["run_error_code"] = string(failure.Reason)
		}
		fields["error_result_present"] = failure.Result != nil
		if result == nil {
			result = failure.Result
		}
	}
	if result != nil {
		fields["text_nonempty"] = strings.TrimSpace(result.Text) != ""
		if marker != "" {
			fields["text_exact_after_trim"] = strings.TrimSpace(result.Text) == marker
			fields["text_contains_marker"] = strings.Contains(result.Text, marker)
		}
		fields["terminal_present"] = result.Raw().Terminal != nil
		if session != "" {
			fields["terminal_check"] = alignmentLiveTerminalFailure(result.Raw().Terminal, session)
		}
	}
	starts, completed, failed, other := 0, 0, 0, 0
	for _, event := range events {
		if e, ok := event.(adaptor.CapabilityInvocation); ok && e.Invocation.Ref.Kind == capability.MCP {
			switch e.Invocation.Phase {
			case capability.Started:
				starts++
			case capability.Completed:
				completed++
			case capability.Failed:
				failed++
			default:
				other++
			}
		}
	}
	fields["mcp_started"] = starts
	fields["mcp_completed"] = completed
	fields["mcp_failed"] = failed
	fields["mcp_other_terminal"] = other
	output = append(output, fields)
	if result == nil {
		return output
	}
	indices := map[string]int{}
	index := func(id string) int {
		if id == "" {
			return 0
		}
		if n := indices[id]; n > 0 {
			return n
		}
		n := len(indices) + 1
		indices[id] = n
		return n
	}
	for _, line := range strings.Split(result.Raw().Stdout, "\n") {
		var outer map[string]any
		if json.Unmarshal([]byte(line), &outer) != nil {
			continue
		}
		typ, _ := outer["type"].(string)
		if typ == "system" && outer["subtype"] == "init" {
			listed, connected, probe, deferTool, search := false, false, false, false, false
			servers, _ := outer["mcp_servers"].([]any)
			for _, item := range servers {
				server, _ := item.(map[string]any)
				if server["name"] == "agent-adaptor-tools" {
					listed = true
					connected = server["status"] == "connected"
				}
			}
			tools, _ := outer["tools"].([]any)
			for _, value := range tools {
				name, _ := value.(string)
				probe = probe || alignmentLiveDiagnosticToolClass(name) == "hosted_history_probe"
				deferTool = deferTool || name == "DeferExecuteTool"
				search = search || name == "ToolSearch"
			}
			entry := map[string]any{"stage": stage, "kind": "formal_init", "hosted_server_present": listed, "hosted_server_connected": connected, "hosted_probe_listed": probe, "defer_tool_listed": deferTool, "tool_search_listed": search}
			if session != "" {
				entry["init_session_match"] = outer["session_id"] == session
			}
			output = append(output, entry)
		}
		if typ != "assistant" && typ != "user" {
			continue
		}
		message, _ := outer["message"].(map[string]any)
		blocks, _ := message["content"].([]any)
		for _, value := range blocks {
			block, _ := value.(map[string]any)
			bt, _ := block["type"].(string)
			if bt != "tool_use" && bt != "tool_result" {
				continue
			}
			id, _ := block["id"].(string)
			if bt == "tool_result" {
				id, _ = block["tool_use_id"].(string)
			}
			scope := "root"
			if parent := outer["parent_tool_use_id"]; parent != nil {
				p, ok := parent.(string)
				if !ok {
					scope = "malformed"
				} else if p != "" {
					if p == id {
						scope = "self"
					} else {
						scope = "foreign"
					}
				}
			}
			entry := map[string]any{"stage": stage, "kind": "formal_tool", "wrapper": typ, "block": bt, "call_index": index(id), "parent_scope": scope}
			if bt == "tool_use" {
				name, _ := block["name"].(string)
				entry["tool_class"] = alignmentLiveDiagnosticToolClass(name)
				input, ok := block["input"].(map[string]any)
				entry["input_object"] = ok
				keys, count := alignmentLiveDiagnosticKeys(input)
				entry["input_keys"] = keys
				entry["unknown_input_keys"] = count
				target, _ := input["toolName"].(string)
				entry["target_tool_class"] = alignmentLiveDiagnosticToolClass(target)
				entry["phase_param"] = alignmentLiveDiagnosticPhase(input["phase"])
				params, ok := input["params"].(map[string]any)
				entry["params_object"] = ok
				keys, count = alignmentLiveDiagnosticKeys(params)
				entry["params_keys"] = keys
				entry["unknown_params_keys"] = count
				entry["params_phase"] = alignmentLiveDiagnosticPhase(params["phase"])
				if name == "DeferExecuteTool" && target == "mcp__agent-adaptor-tools__alignment_echo" {
					value, present := params["text"]
					text, isString := value.(string)
					entry["text_present"] = present
					entry["text_is_string"] = isString
					entry["text_exact_verify"] = isString && text == "VERIFY"
				}
			} else {
				v, exists := block["is_error"]
				flag, ok := v.(bool)
				entry["error_present"] = exists
				entry["error_boolean"] = ok
				entry["error_true"] = flag
			}
			output = append(output, entry)
		}
	}
	return output
}

func TestCodeBuddyCatalogDiagnosticTextPredicates(t *testing.T) {
	const secret = "PRIVATE_CATALOG_CANARY"
	for _, tc := range []struct {
		name              string
		tool              string
		target            any
		params            any
		omitParams        bool
		omitTarget        bool
		project           bool
		present, isString bool
		exact             bool
	}{
		{name: "missing", params: map[string]any{secret: secret}, project: true},
		{name: "missing params", omitParams: true, project: true},
		{name: "null params", params: nil, project: true},
		{name: "string params", params: secret, project: true},
		{name: "null text", params: map[string]any{"text": nil}, project: true, present: true},
		{name: "boolean text", params: map[string]any{"text": true}, project: true, present: true},
		{name: "number text", params: map[string]any{"text": 7}, project: true, present: true},
		{name: "nested text", params: map[string]any{"text": map[string]any{"text": "VERIFY", secret: secret}}, project: true, present: true},
		{name: "array text", params: map[string]any{"text": []any{"VERIFY", secret}}, project: true, present: true},
		{name: "private string", params: map[string]any{"text": secret}, project: true, present: true, isString: true},
		{name: "empty string", params: map[string]any{"text": ""}, project: true, present: true, isString: true},
		{name: "nonexact string", params: map[string]any{"text": "VERIFY "}, project: true, present: true, isString: true},
		{name: "exact string", params: map[string]any{"text": "VERIFY"}, project: true, present: true, isString: true, exact: true},
		{name: "unknown target", target: secret, params: map[string]any{"text": "VERIFY"}},
		{name: "missing target", omitTarget: true, params: map[string]any{"text": "VERIFY"}},
		{name: "empty target", target: "", params: map[string]any{"text": "VERIFY"}},
		{name: "nonstring target", target: 7, params: map[string]any{"text": "VERIFY"}},
		{name: "unknown tool", tool: secret, params: map[string]any{"text": "VERIFY"}},
		{name: "direct tool is not deferred params", tool: "mcp__agent-adaptor-tools__alignment_echo", params: map[string]any{"text": "VERIFY"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, target := tc.tool, tc.target
			if name == "" {
				name = "DeferExecuteTool"
			}
			if target == nil {
				target = "mcp__agent-adaptor-tools__alignment_echo"
			}
			input := map[string]any{"toolName": target, "params": tc.params, secret: secret}
			if tc.omitParams {
				delete(input, "params")
			}
			if tc.omitTarget {
				delete(input, "toolName")
			}
			line, err := json.Marshal(map[string]any{"type": "assistant", "parent_tool_use_id": secret, "message": map[string]any{"content": []any{
				map[string]any{"type": "text", "text": secret},
				map[string]any{"type": "tool_use", "id": secret, "name": name, "input": input},
			}}})
			if err != nil {
				t.Fatal("encode synthetic formal input")
			}
			home := t.TempDir()
			d := alignmentDiagnosticResultDriver{configuredDriver: Driver(Config{CommonConfig: CommonConfig{Command: filepath.Join(home, "no-provider"), Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CODEBUDDY_CONFIG_DIR", Value: home}}}}).(configuredDriver), response: driver.Response{Output: secret, RawStreams: &driver.RawStreams{Stdout: string(line), Stderr: secret}}}
			a := adaptor.New(d)
			defer a.Close(context.Background())
			r, err := a.Run(context.Background(), "synthetic catalog diagnostic")
			if err != nil {
				t.Fatal("synthetic result unavailable")
			}
			facts := alignmentLiveDiagnosticFacts("catalog", r, nil, "", "", 0, 0, nil)
			if len(facts) != 2 || facts[1]["kind"] != "formal_tool" {
				t.Fatal("formal tool projection missing")
			}
			for key, want := range map[string]bool{"text_present": tc.present, "text_is_string": tc.isString, "text_exact_verify": tc.exact} {
				got, exists := facts[1][key]
				if exists != tc.project || exists && got != want {
					t.Fatalf("safe %s predicate or target boundary changed", key)
				}
			}
			raw, err := json.Marshal(facts)
			if err != nil || strings.Contains(string(raw), secret) || strings.Contains(string(raw), "VERIFY") {
				t.Fatal("catalog diagnostic disclosed input/body/name/ID")
			}
		})
	}
}

func TestCodeBuddyCatalogDiagnosticCallbackCounts(t *testing.T) {
	const secret = "PRIVATE_CALLBACK_CANARY"
	var totalEcho, actualEcho atomic.Int32
	definition := alignmentCatalogEchoDefinition(&totalEcho, &actualEcho)
	for i, tc := range []struct {
		text  string
		exact int
	}{{secret, 0}, {"VERIFY ", 0}, {"VERIFY", 1}, {"", 1}, {"VERIFY", 2}} {
		input, err := json.Marshal(map[string]string{"text": tc.text})
		if err != nil {
			t.Fatal("encode synthetic callback input")
		}
		output, err := definition.Invoke(context.Background(), input)
		var echoed string
		if err != nil || json.Unmarshal(output, &echoed) != nil || echoed != tc.text {
			t.Fatal("original callback echo behavior changed")
		}
		facts := alignmentCatalogCallbackDiagnostic(int(totalEcho.Load()), int(actualEcho.Load()))
		if len(facts) != 4 || facts["stage"] != "catalog" || facts["kind"] != "catalog_callback" || facts["total"] != i+1 || facts["exact_verify"] != tc.exact {
			t.Fatal("total and exact VERIFY callback counts were conflated")
		}
		raw, err := json.Marshal(facts)
		if err != nil || strings.Contains(string(raw), secret) || strings.Contains(string(raw), "VERIFY") {
			t.Fatal("callback diagnostic disclosed input")
		}
	}
	if _, err := definition.Invoke(context.Background(), json.RawMessage(`{"text":7}`)); err == nil || totalEcho.Load() != 5 || actualEcho.Load() != 2 {
		t.Fatal("invalid typed input changed actual callback counts")
	}
}
