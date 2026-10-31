package pi

import (
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

const toolPrefix = `{"type":"session","id":"pi-session"}
{"type":"agent_start"}
{"type":"message_start","message":{"role":"assistant","content":[]}}
{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":0,"id":"call-read","toolName":"read"}}
{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":0,"delta":"{\"path\":"}}
{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":0,"delta":"\"file.txt\"}"}}
{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":0,"toolCall":{"type":"toolCall","id":"call-read","name":"read","arguments":{"path":"file.txt"}}}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-read","name":"read","arguments":{"path":"file.txt"}}],"stopReason":"toolUse"}}
{"type":"tool_execution_start","toolCallId":"call-read","toolName":"read","args":{"path":"file.txt"}}
{"type":"tool_execution_end","toolCallId":"call-read","toolName":"read","result":{"content":[{"type":"text","text":"file content"}],"details":{"line":1},"nativeExtra":"retained"},"isError":false}
`

func TestPiToolArgumentStreaming(t *testing.T) {
	raw := toolPrefix + strings.Join(strings.Split(happy, "\n")[2:], "\n")
	sink := &testutil.EventRecorder{}
	p := newParser(sink, true)
	p.events.Stream(driver.StreamPayload{Kind: driver.StreamRunStarted})
	for _, b := range []byte(raw) {
		p.lines.Write([]byte{b}, p.line)
	}
	p.lines.Flush(p.line)
	response := p.finish(clihelper.CommandResult{}, nil)
	if response.Failure != nil || response.Checkpoint == nil || response.Output != "OK" {
		t.Fatalf("response=%+v", response)
	}
	var args string
	starts, ends, results := 0, 0, 0
	for _, event := range sink.StreamSnapshot() {
		switch event.Kind {
		case driver.StreamToolCallStart:
			starts++
		case driver.StreamToolCallArgs:
			args += event.Delta
		case driver.StreamToolCallEnd:
			ends++
		case driver.StreamToolCallResult:
			results++
			if event.Result["nativeExtra"] != "retained" {
				t.Fatal("provider result fields lost")
			}
		}
	}
	if starts != 1 || ends != 1 || results != 1 || args != `{"path":"file.txt"}` {
		t.Fatalf("starts=%d ends=%d results=%d args=%s", starts, ends, results, args)
	}
	for _, v := range adaptertest.VerifyStreamSequence(sink.StreamSnapshot()) {
		t.Error(v)
	}
	for _, bad := range []string{
		strings.Replace(raw, `"toolName":"read"`, `"toolName":""`, 1),
		strings.Replace(raw, `"type":"toolcall_end","contentIndex":0`, `"type":"toolcall_end","contentIndex":1`, 1),
		strings.Replace(raw, `"type":"toolcall_start"`, `"type":"future_event"`, 1),
	} {
		p := newParser(&testutil.EventRecorder{}, true)
		p.lines.Write([]byte(bad), p.line)
		p.lines.Flush(p.line)
		resp := p.finish(clihelper.CommandResult{}, nil)
		if resp.Checkpoint != nil || resp.Failure == nil {
			t.Fatal("malformed tool lifecycle created a checkpoint")
		}
	}
}
