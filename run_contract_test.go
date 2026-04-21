package agentadaptor_test

import (
	"context"
	"reflect"
	"sort"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type rawStreamsDriver struct{}

func (rawStreamsDriver) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{Type: "rawstreams-test", DisplayName: "RawStreams Test"}
}

func (rawStreamsDriver) ValidateConfig(cfg any) error { return nil }

func (rawStreamsDriver) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	// Emit a stable set of transcript items to validate the Events -> Transcript
	// invariant without taking any dependency on clihelper.
	items := []agentadaptor.TranscriptItem{
		{Kind: agentadaptor.TranscriptInit, SessionID: "sess-1"},
		{Kind: agentadaptor.TranscriptAssistant, Text: "Hello from the raw streams driver."},
		{Kind: agentadaptor.TranscriptResult, Subtype: "completion"},
	}
	for _, item := range items {
		clone := item
		_ = sink.Emit(agentadaptor.RunEvent{
			Type: agentadaptor.RunEventItem,
			Item: &clone,
		})
	}

	return agentadaptor.DriverRunResult{
		Output:     "Hello from the raw streams driver.",
		RawStreams: &agentadaptor.RawStreams{Stdout: "raw-stdout-bytes", Stderr: "raw-stderr-bytes"},
		Transcript: items,
		ExitCode:   0,
		Summary:    "Hello from the raw streams driver.",
	}, nil
}

func TestRunExposesRawStreamsFromAdapter(t *testing.T) {
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(rawStreamsDriver{}, struct{}{})),
	)

	result, err := sdk.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.RawStreams == nil {
		t.Fatal("expected RawStreams to be populated on Run()")
	}
	if result.RawStreams.Stdout != "raw-stdout-bytes" || result.RawStreams.Stderr != "raw-stderr-bytes" {
		t.Fatalf("unexpected raw streams: %#v", result.RawStreams)
	}
	if result.Output != "Hello from the raw streams driver." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestStartEventsMatchRunResultTranscript(t *testing.T) {
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(rawStreamsDriver{}, struct{}{})),
	)

	handle, err := sdk.Start(context.Background(), "hi")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	type seqItem struct {
		seq  uint64
		item agentadaptor.TranscriptItem
	}
	var collected []seqItem
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for event := range handle.Events() {
			if event.Seq == 0 {
				t.Errorf("expected non-zero Seq for event %+v", event)
			}
			if event.Type == agentadaptor.RunEventItem && event.Item != nil {
				collected = append(collected, seqItem{seq: event.Seq, item: *event.Item})
			}
		}
	}()

	result, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	<-eventsDone

	sort.SliceStable(collected, func(i, j int) bool { return collected[i].seq < collected[j].seq })
	items := make([]agentadaptor.TranscriptItem, 0, len(collected))
	for _, entry := range collected {
		items = append(items, entry.item)
	}
	if !reflect.DeepEqual(items, result.Transcript) {
		t.Fatalf("transcript diverged between Events and Wait():\n  events  = %#v\n  result = %#v", items, result.Transcript)
	}
	if result.RawStreams == nil || result.RawStreams.Stdout != "raw-stdout-bytes" {
		t.Fatalf("expected RawStreams on Start().Wait(), got %#v", result.RawStreams)
	}
}
