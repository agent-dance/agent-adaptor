package a2a

import (
	"encoding/json"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestHostMessageImagesAndIdentityStayOutsideA2AWire(t *testing.T) {
	for _, exposure := range []ExposurePolicy{{}, {
		IncludeReasoning: true, IncludeToolCalls: true, IncludeHITL: true,
		IncludeCapabilityInvocations: true, IncludeTodos: true,
		Diagnostics: DiagnosticsPolicy{IncludeMetadata: true, IncludeUsage: true,
			IncludeProviderResult: true, IncludeTranscript: true, IncludeRawStreams: true,
			IncludeHITLPayloads: true, IncludeHITLRaw: true},
	}} {
		tr := newStreamTranslator(testTaskInfo{}, exposure)
		image := adaptor.ImageContent{MessageID: "m", Type: "binary", MIMEType: "image/png", URL: "/host-only", Role: adaptor.RoleUser, UserID: "host-user"}
		if out := tr.Translate(image); len(out) != 0 {
			t.Fatal("unsupported image unexpectedly crossed authentication domain")
		}
		text := adaptor.TextDelta{MessageID: "m", Text: "question", Role: adaptor.RoleUser, UserID: "host-user"}
		out := tr.Translate(text)
		if len(out) != 1 {
			t.Fatal("existing text projection lost")
		}
		wire, err := json.Marshal(dataValue(t, out[0]))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wire), "host-user") || strings.Contains(string(wire), "user_id") || strings.Contains(string(wire), "UserID") {
			t.Fatalf("host identity crossed: %s", wire)
		}
		event, matched, err := DecodeAdapterEventV1(dataValue(t, out[0]))
		if err != nil || !matched {
			t.Fatalf("text decode failed: %v", err)
		}
		got := event.(adaptor.TextDelta)
		if got.Text != "question" || got.Role != adaptor.RoleUser || got.UserID != "" {
			t.Fatalf("text changed: %+v", got)
		}
	}
}
