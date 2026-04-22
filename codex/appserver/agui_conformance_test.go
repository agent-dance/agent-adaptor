package appserver

import (
	"encoding/json"
	"testing"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
)

func translateAppserverToAGUI(t *testing.T, payloads []agentadaptor.StreamPayload) []aguievents.Event {
	t.Helper()
	tr := agui.NewTranslator()
	var out []aguievents.Event
	for _, p := range payloads {
		out = append(out, tr.Translate(p)...)
	}
	return out
}

func mustVerifyAGUI(t *testing.T, evs []aguievents.Event) {
	t.Helper()
	if err := agui.VerifySequence(evs); err != nil {
		t.Fatalf("AG-UI VerifySequence: %v", err)
	}
}

// recordingSink is defined in appserver_test.go

func TestCodexTranslatorCommandExecutionToAGUIConformant(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-cmd")
	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t-cmd"}}`))
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t-cmd","turn":{"id":"turn-1","status":"inProgress"}}`))
	tr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"t-cmd","turnId":"turn-1","item":{"id":"item-cmd-1","type":"commandExecution","command":"ls","cwd":"/","status":"completed","exitCode":0,"aggregatedOutput":"out"}}`))
	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t-cmd","turnId":"turn-1","item":{"id":"item-cmd-1","type":"commandExecution","command":"ls","cwd":"/","status":"completed","exitCode":0,"aggregatedOutput":"out\n"}}`))
	tr.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"t-cmd","turn":{"id":"turn-1","status":"completed"}}`))

	evs := translateAppserverToAGUI(t, sink.streams)
	mustVerifyAGUI(t, evs)
}

func TestCodexTranslatorFileChangeToAGUIConformant(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-fc")
	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t-fc"}}`))
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t-fc","turn":{"id":"turn-1","status":"inProgress"}}`))
	tr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"t-fc","turnId":"turn-1","item":{"id":"fc-1","type":"fileChange","status":"completed","changes":[{"path":"a.go","diff":"+x"}]}}`))
	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t-fc","turnId":"turn-1","item":{"id":"fc-1","type":"fileChange","status":"completed","changes":[{"path":"a.go","diff":"+x"}]}}`))
	tr.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"t-fc","turn":{"id":"turn-1","status":"completed"}}`))
	evs := translateAppserverToAGUI(t, sink.streams)
	mustVerifyAGUI(t, evs)
}

func TestCodexTranslatorMcpToolToAGUIConformant(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-mcp")
	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t-mcp"}}`))
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t-mcp","turn":{"id":"turn-1","status":"inProgress"}}`))
	tr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"t-mcp","turnId":"turn-1","item":{"id":"mcp-1","type":"mcpToolCall","server":"s","tool":"t","status":"completed","arguments":{},"result":"ok"}}`))
	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t-mcp","turnId":"turn-1","item":{"id":"mcp-1","type":"mcpToolCall","server":"s","tool":"t","status":"completed","result":"ok"}}`))
	tr.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"t-mcp","turn":{"id":"turn-1","status":"completed"}}`))
	evs := translateAppserverToAGUI(t, sink.streams)
	mustVerifyAGUI(t, evs)
}

func TestCodexTranslatorWebSearchToAGUIConformant(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-ws")
	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t-ws"}}`))
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t-ws","turn":{"id":"turn-1","status":"inProgress"}}`))
	tr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"t-ws","turnId":"turn-1","item":{"id":"ws-1","type":"webSearch","query":"q"}}`))
	// No StreamToolCallResult: emitToolEnd with nil
	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t-ws","turnId":"turn-1","item":{"id":"ws-1","type":"webSearch","query":"q"}}`))
	tr.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"t-ws","turn":{"id":"turn-1","status":"completed"}}`))
	evs := translateAppserverToAGUI(t, sink.streams)
	mustVerifyAGUI(t, evs)
}

func TestCodexTranslatorDynamicToolToAGUIConformant(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-dyn")
	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t-dyn"}}`))
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t-dyn","turn":{"id":"turn-1","status":"inProgress"}}`))
	tr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"t-dyn","turnId":"turn-1","item":{"id":"dyn-1","type":"dynamicToolCall","tool":"myTool","status":"completed"}}`))
	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t-dyn","turnId":"turn-1","item":{"id":"dyn-1","type":"dynamicToolCall","tool":"myTool","status":"completed"}}`))
	tr.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"t-dyn","turn":{"id":"turn-1","status":"completed"}}`))
	evs := translateAppserverToAGUI(t, sink.streams)
	mustVerifyAGUI(t, evs)
}

func TestCodexTranslatorCommandOutputDeltaBeforeItemStarted_AGUIConformant(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-reorder")
	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t-ro"}}`))
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t-ro","turn":{"id":"turn-1","status":"inProgress"}}`))
	// Deltas can arrive before item/started
	tr.Dispatch(NotifyItemCommandExecutionOutputDelta, json.RawMessage(`{"threadId":"t-ro","turnId":"turn-1","itemId":"cmd-early","delta":"line1"}`))
	tr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"t-ro","turnId":"turn-1","item":{"id":"cmd-early","type":"commandExecution","command":"ls","cwd":"/","status":"running"}}`))
	tr.Dispatch(NotifyItemCommandExecutionOutputDelta, json.RawMessage(`{"threadId":"t-ro","turnId":"turn-1","itemId":"cmd-early","delta":"line2"}`))
	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t-ro","turnId":"turn-1","item":{"id":"cmd-early","type":"commandExecution","command":"ls","cwd":"/","status":"completed","exitCode":0,"aggregatedOutput":"line1line2"}}`))
	tr.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"t-ro","turn":{"id":"turn-1","status":"completed"}}`))
	evs := translateAppserverToAGUI(t, sink.streams)
	mustVerifyAGUI(t, evs)
}

func TestCodexTranslatorTurnFailedToAGUIConformant(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-fail")
	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t-fail"}}`))
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t-fail","turn":{"id":"turn-1","status":"inProgress"}}`))
	tr.Dispatch(NotifyTurnFailed, json.RawMessage(`{"threadId":"t-fail","turn":{"id":"turn-1","error":"boom detail"}}`))
	evs := translateAppserverToAGUI(t, sink.streams)
	mustVerifyAGUI(t, evs)
}

func TestCodexTranslatorErrorNotificationToAGUIConformant(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-errn")
	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t-errn"}}`))
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t-errn","turn":{"id":"turn-1","status":"inProgress"}}`))
	tr.Dispatch(NotifyError, json.RawMessage(`{"error":{"message":"e","type":"E"}}`))
	evs := translateAppserverToAGUI(t, sink.streams)
	mustVerifyAGUI(t, evs)
}
