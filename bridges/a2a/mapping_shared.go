package a2a

import (
	"fmt"
	"strings"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
)

func customTerminalArtifacts(info a2aproto.TaskInfoProvider, artifacts []ArtifactSpec, reserved map[string]struct{}) ([]a2aproto.Event, error) {
	if len(artifacts) == 0 {
		return nil, nil
	}
	out := make([]a2aproto.Event, 0, len(artifacts))
	seen := make(map[string]struct{}, len(artifacts))
	for i, spec := range artifacts {
		id := strings.TrimSpace(defaultString(spec.ID, spec.Name))
		if id == "" {
			return nil, fmt.Errorf("custom artifact %d requires ID or Name", i)
		}
		if _, ok := reserved[id]; ok {
			return nil, fmt.Errorf("custom artifact id %q is reserved by the bridge", id)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("duplicate custom artifact id %q", id)
		}
		seen[id] = struct{}{}
		parts, err := outboundParts(spec.Parts)
		if err != nil {
			return nil, fmt.Errorf("custom artifact %q: %w", id, err)
		}
		metadata, err := normalizeJSONMap(spec.Metadata)
		if err != nil {
			return nil, fmt.Errorf("custom artifact %q metadata: %w", id, err)
		}
		ev := a2aproto.NewArtifactUpdateEvent(info, a2aproto.ArtifactID(id), parts...)
		ev.Append = false
		ev.LastChunk = true
		ev.Artifact.ID = a2aproto.ArtifactID(id)
		ev.Artifact.Name = defaultString(spec.Name, id)
		ev.Artifact.Description = spec.Description
		ev.Artifact.Extensions = append([]string(nil), spec.Extensions...)
		ev.Artifact.Metadata = metadata
		out = append(out, ev)
	}
	return out, nil
}

func reservedArtifactIDs(replaceDefault bool) map[string]struct{} {
	reserved := map[string]struct{}{}
	if !replaceDefault {
		reserved[ArtifactAgentAdaptorResult] = struct{}{}
	}
	return reserved
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (p ExposurePolicy) hasStreamingDiagnostics() bool {
	return p.IncludeReasoning || p.IncludeToolCalls || p.IncludeHITL ||
		p.Diagnostics.IncludeMetadata || p.Diagnostics.IncludeUsage ||
		p.Diagnostics.IncludeProviderResult || p.Diagnostics.IncludeTranscript ||
		p.Diagnostics.IncludeRawStreams
}
