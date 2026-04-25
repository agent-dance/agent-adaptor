package providers_test

import (
	"context"
	"errors"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/providers"
)

// staticProvider is a tiny test fixture implementing the v0.5
// SkillProvider (and optionally SkillCatalog) by returning a fixed
// slice. GetSkills filters by requested keys; Catalogue returns the
// entire fixture verbatim.
type staticProvider struct {
	skills []agentadaptor.Skill
	err    error
}

func (s staticProvider) GetSkills(_ context.Context, keys []string) (map[string]agentadaptor.Skill, error) {
	if s.err != nil {
		return nil, s.err
	}
	want := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
	}
	out := make(map[string]agentadaptor.Skill, len(s.skills))
	for _, skill := range s.skills {
		if _, requested := want[skill.Key]; requested || skill.Required {
			out[skill.Key] = skill
		}
	}
	return out, nil
}

// catalogStaticProvider extends staticProvider with the Catalogue method
// so it doubles as a SkillCatalog. Used by the test that needs Catalogue
// pin-application to fire.
type catalogStaticProvider struct {
	staticProvider
}

func (c catalogStaticProvider) Catalogue(_ context.Context) ([]agentadaptor.Skill, error) {
	if c.err != nil {
		return nil, c.err
	}
	out := make([]agentadaptor.Skill, len(c.skills))
	copy(out, c.skills)
	return out, nil
}

func keysOf(skills []agentadaptor.Skill) []string {
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Key)
	}
	return out
}

func TestMarkRequiredPromotesMatchingSkillsAndPreservesOthers(t *testing.T) {
	t.Parallel()
	upstream := staticProvider{
		skills: []agentadaptor.Skill{
			{Key: "compliance", Source: agentadaptor.SkillFromInline{SkillMD: "# compliance"}},
			{Key: "style-guide", Source: agentadaptor.SkillFromInline{SkillMD: "# style"}},
		},
	}
	wrapped := providers.MarkRequired(upstream,
		providers.Pin{Key: "compliance", Reason: "SOC2"},
	)

	got, err := wrapped.GetSkills(context.Background(), keysOf(upstream.skills))
	if err != nil {
		t.Fatalf("GetSkills: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(got))
	}
	compliance := got["compliance"]
	style := got["style-guide"]
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
	t.Parallel()
	upstream := staticProvider{
		skills: []agentadaptor.Skill{
			{Key: "compliance", Source: agentadaptor.SkillFromInline{SkillMD: "# compliance"}},
		},
	}
	wrapped := providers.MarkRequired(upstream,
		providers.Pin{Key: "", Reason: "ignored"},
		providers.Pin{Key: "missing", Reason: "also ignored"},
	)
	got, err := wrapped.GetSkills(context.Background(), []string{"compliance"})
	if err != nil {
		t.Fatalf("GetSkills: %v", err)
	}
	if len(got) != 1 || got["compliance"].Required {
		t.Fatalf("expected single non-promoted skill, got %#v", got)
	}
}

func TestMarkRequiredLeavesReasonUntouchedWhenPinReasonEmpty(t *testing.T) {
	t.Parallel()
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
	got, err := wrapped.GetSkills(context.Background(), []string{"compliance"})
	if err != nil {
		t.Fatalf("GetSkills: %v", err)
	}
	skill, ok := got["compliance"]
	if !ok || !skill.Required {
		t.Fatalf("expected compliance to be required, got %#v", got)
	}
	if skill.Reason != "upstream reason" {
		t.Fatalf("expected upstream reason to be preserved, got %q", skill.Reason)
	}
}

func TestMarkRequiredForwardsProviderErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("upstream failure")
	wrapped := providers.MarkRequired(staticProvider{err: sentinel})
	if _, err := wrapped.GetSkills(context.Background(), []string{"x"}); !errors.Is(err, sentinel) {
		t.Fatalf("expected upstream error to propagate, got %v", err)
	}
}

func TestMarkRequiredHandlesNilInner(t *testing.T) {
	t.Parallel()
	wrapped := providers.MarkRequired(nil, providers.Pin{Key: "compliance"})
	got, err := wrapped.GetSkills(context.Background(), []string{"compliance"})
	if err != nil {
		t.Fatalf("GetSkills: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty result for nil inner, got %#v", got)
	}
}

// TestMarkRequiredCataloguePromotesEntries covers the SkillCatalog
// extension: when the upstream provider exposes Catalogue (e.g. an
// admin-style enumerable provider), MarkRequired's wrapper must
// promote matching pins on that path too — otherwise Admin.ListSkills
// would show pinned skills as non-required despite the decorator.
func TestMarkRequiredCataloguePromotesEntries(t *testing.T) {
	t.Parallel()
	upstream := catalogStaticProvider{
		staticProvider: staticProvider{
			skills: []agentadaptor.Skill{
				{Key: "compliance", Source: agentadaptor.SkillFromInline{SkillMD: "# c"}},
				{Key: "style", Source: agentadaptor.SkillFromInline{SkillMD: "# s"}},
			},
		},
	}
	wrapped := providers.MarkRequired(upstream,
		providers.Pin{Key: "compliance", Reason: "SOC2"},
	)
	cat, ok := wrapped.(agentadaptor.SkillCatalog)
	if !ok {
		t.Fatalf("expected SkillCatalog passthrough; wrapper does not implement it")
	}
	skills, err := cat.Catalogue(context.Background())
	if err != nil {
		t.Fatalf("Catalogue: %v", err)
	}
	var found agentadaptor.Skill
	for _, s := range skills {
		if s.Key == "compliance" {
			found = s
		}
	}
	if !found.Required || found.Reason != "SOC2" {
		t.Fatalf("expected compliance promoted in catalogue: got %#v", found)
	}
}

// TestMarkRequiredNonCatalogStaysProvider verifies that the
// constructor does NOT advertise SkillCatalog when inner is a plain
// SkillProvider — admin paths must correctly downgrade.
func TestMarkRequiredNonCatalogStaysProvider(t *testing.T) {
	t.Parallel()
	upstream := staticProvider{
		skills: []agentadaptor.Skill{
			{Key: "compliance", Source: agentadaptor.SkillFromInline{SkillMD: "# c"}},
		},
	}
	wrapped := providers.MarkRequired(upstream, providers.Pin{Key: "compliance"})
	if _, ok := wrapped.(agentadaptor.SkillCatalog); ok {
		t.Fatalf("MarkRequired must not pretend to be a catalog when inner isn't one")
	}
}
