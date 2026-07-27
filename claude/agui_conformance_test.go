package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/driver"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// claudeFixtureDriver keeps these provider conformance tests on the real v1
// path: Claude's formal-protocol parser emits the Driver SPI, next turns that
// SPI into its one typed Event stream, and bridges/agui consumes that Stream.
// It deliberately does not call a translator with legacy StreamPayloads.
type claudeFixtureDriver struct {
	stdout []byte
}

func (claudeFixtureDriver) Descriptor() driver.Descriptor {
	return adapter{}.Descriptor()
}

func (claudeFixtureDriver) ValidateConfig(any) error { return nil }

func (d claudeFixtureDriver) Run(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	parser := newClaudeParser(sink)
	parser.enableStreaming(req.RunID)
	if err := parser.onChunk("stdout", d.stdout, time.Now().UTC()); err != nil {
		return driver.Response{}, err
	}
	parser.finalize()

	var failure *driver.RunFailure
	switch {
	case parser.pendingFailure != nil:
		copy := *parser.pendingFailure
		failure = &copy
	case strings.TrimSpace(parser.errorMessage) != "":
		failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: parser.errorMessage}
	case !parser.terminalSeen || !parser.terminalSuccess:
		failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: "claude protocol ended without a successful terminal result"}
	}

	raw := driver.RawStreams{
		Stdout:   string(d.stdout),
		Terminal: parser.terminal,
	}
	return driver.Response{
		Output:     parser.buildOutput(),
		Summary:    parser.finalSummary(),
		RawStreams: &raw,
		Transcript: parser.transcript,
		Usage:      parser.usage,
		Checkpoint: parser.checkpointForOutcome(0, "", false, failure),
		Provider:   "anthropic",
		Failure:    failure,
	}, nil
}

func mustVerifyAGUI(t *testing.T, events []aguievents.Event) {
	t.Helper()
	if err := agui.VerifySequence(events); err != nil {
		t.Fatalf("AG-UI VerifySequence: %v", err)
	}
}

func testParserFixtureToAGUI(t *testing.T, filename string) []aguievents.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatal(err)
	}

	agent := adaptor.New(claudeFixtureDriver{stdout: data})
	stream := agent.Stream(context.Background(), "fixture")
	events := make([]aguievents.Event, 0, 16)
	for event := range agui.Events(stream) {
		events = append(events, event)
	}
	return events
}

func TestClaudeParserStreamingToolUseFixture_AGUIConformant(t *testing.T) {
	events := testParserFixtureToAGUI(t, "streaming-tool-use.jsonl")
	mustVerifyAGUI(t, events)
}

func TestClaudeParserStreamingHappyFixture_AGUIConformant(t *testing.T) {
	events := testParserFixtureToAGUI(t, "streaming-happy.jsonl")
	mustVerifyAGUI(t, events)
}

func TestClaudeParserPermissionAndResult_AGUIConformant(t *testing.T) {
	events := testParserFixtureToAGUI(t, "streaming-permission-agui.jsonl")
	mustVerifyAGUI(t, events)
}

func TestClaudeParserErrorTerminal_AGUIConformant(t *testing.T) {
	events := testParserFixtureToAGUI(t, "streaming-error-agui.jsonl")
	mustVerifyAGUI(t, events)
}

func TestClaudeParserTruncatedStream_AGUIConformant(t *testing.T) {
	events := testParserFixtureToAGUI(t, "streaming-truncated-agui.jsonl")
	mustVerifyAGUI(t, events)
}

func TestClaudeParserUserToolMissingID_AGUIConformant(t *testing.T) {
	events := testParserFixtureToAGUI(t, "streaming-user-tool-missing-id-agui.jsonl")
	mustVerifyAGUI(t, events)
}
