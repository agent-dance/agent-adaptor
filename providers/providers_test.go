package providers_test

import (
	"context"
	"errors"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/providers"
)

type staticProvider struct {
	skills []agentadaptor.Skill
	err    error
}

func (s staticProvider) List(_ context.Context, _ string) ([]agentadaptor.Skill, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]agentadaptor.Skill, len(s.skills))
	copy(out, s.skills)
	return out, nil
}

func TestMarkRequiredPromotesMatchingSkillsAndPreservesOthers(t *testing.T) {
	upstream := staticProvider{
		skills: []agentadaptor.Skill{
			{Key: "compliance", Source: agentadaptor.SkillFromInline{SkillMD: "# compliance"}},
			{Key: "style-guide", Source: agentadaptor.SkillFromInline{SkillMD: "# style"}},
		},
	}
	wrapped := providers.MarkRequired(upstream,
		providers.Pin{Key: "compliance", Reason: "SOC2"},
	)

	got, err := wrapped.List(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(got))
	}
	var compliance, style agentadaptor.Skill
	for _, skill := range got {
		switch skill.Key {
		case "compliance":
			compliance = skill
		case "style-guide":
			style = skill
		}
	}
	if !compliance.Required {
		t.Fatalf("expected compliance to be marked Required, got %#v", compliance)
	}
	if compliance.Reason != "SOC2" {
		t.Fatalf("expected compliance reason to be overridden, got %q", compliance.Reason)
	}
	if style.Required {
		t.Fatalf("unrelated skill must not be promoted: %#v", style)
	}
}

func TestMarkRequiredIgnoresUnmatchedPinsAndEmptyKeys(t *testing.T) {
	upstream := staticProvider{
		skills: []agentadaptor.Skill{
			{Key: "compliance", Source: agentadaptor.SkillFromInline{SkillMD: "# compliance"}},
		},
	}
	wrapped := providers.MarkRequired(upstream,
		providers.Pin{Key: "", Reason: "ignored"},
		providers.Pin{Key: "missing", Reason: "also ignored"},
	)
	got, err := wrapped.List(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Required {
		t.Fatalf("expected single non-promoted skill, got %#v", got)
	}
}

func TestMarkRequiredLeavesReasonUntouchedWhenPinReasonEmpty(t *testing.T) {
	upstream := staticProvider{
		skills: []agentadaptor.Skill{
			{
				Key:    "compliance",
				Source: agentadaptor.SkillFromInline{SkillMD: "# compliance"},
				Reason: "upstream reason",
			},
		},
	}
	wrapped := providers.MarkRequired(upstream, providers.Pin{Key: "compliance"})
	got, err := wrapped.List(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || !got[0].Required {
		t.Fatalf("expected compliance to be required, got %#v", got)
	}
	if got[0].Reason != "upstream reason" {
		t.Fatalf("expected upstream reason to be preserved, got %q", got[0].Reason)
	}
}

func TestMarkRequiredForwardsProviderErrors(t *testing.T) {
	sentinel := errors.New("upstream failure")
	wrapped := providers.MarkRequired(staticProvider{err: sentinel})
	if _, err := wrapped.List(context.Background(), "tenant-a"); !errors.Is(err, sentinel) {
		t.Fatalf("expected upstream error to propagate, got %v", err)
	}
}

func TestMarkRequiredHandlesNilInner(t *testing.T) {
	wrapped := providers.MarkRequired(nil, providers.Pin{Key: "compliance"})
	got, err := wrapped.List(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty catalogue for nil inner, got %#v", got)
	}
}
