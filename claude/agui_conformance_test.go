package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
)

func translateAllAGUI(tr *agui.Translator, ps []agentadaptor.StreamPayload) []aguievents.Event {
	var out []aguievents.Event
	for _, p := range ps {
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

func testParserFixtureToAGUI(t *testing.T, filename string) []agentadaptor.StreamPayload {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatal(err)
	}
	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-agui-conform")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()
	return sink.snapshot()
}

func TestClaudeParserStreamingToolUseFixture_AGUIConformant(t *testing.T) {
	ps := testParserFixtureToAGUI(t, "streaming-tool-use.jsonl")
	evs := translateAllAGUI(agui.NewTranslator(), ps)
	mustVerifyAGUI(t, evs)
}

func TestClaudeParserStreamingHappyFixture_AGUIConformant(t *testing.T) {
	ps := testParserFixtureToAGUI(t, "streaming-happy.jsonl")
	evs := translateAllAGUI(agui.NewTranslator(), ps)
	mustVerifyAGUI(t, evs)
}

func TestClaudeParserPermissionAndResult_AGUIConformant(t *testing.T) {
	ps := testParserFixtureToAGUI(t, "streaming-permission-agui.jsonl")
	evs := translateAllAGUI(agui.NewTranslator(), ps)
	mustVerifyAGUI(t, evs)
}

func TestClaudeParserErrorTerminal_AGUIConformant(t *testing.T) {
	ps := testParserFixtureToAGUI(t, "streaming-error-agui.jsonl")
	evs := translateAllAGUI(agui.NewTranslator(), ps)
	mustVerifyAGUI(t, evs)
}

func TestClaudeParserTruncatedStream_AGUIConformant(t *testing.T) {
	ps := testParserFixtureToAGUI(t, "streaming-truncated-agui.jsonl")
	evs := translateAllAGUI(agui.NewTranslator(), ps)
	mustVerifyAGUI(t, evs)
}

func TestClaudeParserUserToolMissingID_AGUIConformant(t *testing.T) {
	ps := testParserFixtureToAGUI(t, "streaming-user-tool-missing-id-agui.jsonl")
	evs := translateAllAGUI(agui.NewTranslator(), ps)
	mustVerifyAGUI(t, evs)
}
