// Package providers hosts small, composable decorators that adapt one
// SkillProvider value into another one. These decorators intentionally live
// outside the main SDK surface so they remain opt-in sugar rather than
// first-class API.
//
// The canonical use case is "the upstream provider is owned by someone else
// and I cannot modify it, but in my deployment skill X must be Required".
// Hosts typically do this at the SkillProvider level (tenant-aware fetch
// returning Required=true from GetSkills) — this package covers the
// degenerate case where that is not possible.
package providers

import (
	"context"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// Pin describes one (Key, Reason) pair the MarkRequired decorator should
// project onto the upstream provider's catalogue.
type Pin struct {
	// Key is the Skill.Key the decorator will match against the upstream
	// provider's GetSkills (and Catalogue, when supported) results.
	Key string
	// Reason is copied onto Skill.Reason when Required is set to true by
	// the decorator. An empty Reason means the decorator leaves the existing
	// Reason alone (so the upstream one wins if present).
	Reason string
}

// MarkRequired wraps inner so that, after every GetSkills / Catalogue
// call, the skills whose Key matches one of the supplied Pin entries
// have their Required flag set to true (and Reason filled if
// Pin.Reason is non-empty).
//
// The wrapping is intentionally additive:
//
//   - Skills outside the pin set are returned verbatim.
//   - Pins that do not match any returned skill are silently ignored.
//     Hosts that want hard validation can compare the inputs themselves.
//   - If inner is nil, the returned provider's GetSkills returns
//     (nil, nil) and the pins are dormant. MarkRequired is a decorator,
//     not a source: it promotes existing skills to Required but never
//     synthesises skill bodies. Callers who want to inject brand-new
//     required skills should register them through binding defaults
//     (WithDefaultSkills) or an inline provider instead.
//
// The decorator forwards every error from inner unchanged. When inner
// also satisfies SkillCatalog, the returned value satisfies
// SkillCatalog as well (so Admin.ListSkills keeps working).
func MarkRequired(inner agentadaptor.SkillProvider, pins ...Pin) agentadaptor.SkillProvider {
	cleaned := make([]Pin, 0, len(pins))
	for _, pin := range pins {
		if pin.Key == "" {
			continue
		}
		cleaned = append(cleaned, pin)
	}
	base := &markRequiredProvider{inner: inner, pins: cleaned}
	if _, ok := inner.(agentadaptor.SkillCatalog); ok {
		return &markRequiredCatalog{markRequiredProvider: base}
	}
	return base
}

type markRequiredProvider struct {
	inner agentadaptor.SkillProvider
	pins  []Pin
}

// GetSkills implements SkillProvider.
//
// Each Skill returned by inner is examined against the pin set;
// matching entries are returned with Required=true (and Reason
// overwritten when Pin.Reason is non-empty). Non-matching entries
// pass through unchanged.
func (p *markRequiredProvider) GetSkills(ctx context.Context, keys []string) (map[string]agentadaptor.Skill, error) {
	if p.inner == nil {
		return nil, nil
	}
	skills, err := p.inner.GetSkills(ctx, keys)
	if err != nil {
		return nil, err
	}
	if len(p.pins) == 0 || len(skills) == 0 {
		return skills, nil
	}
	lookup := pinLookup(p.pins)
	out := make(map[string]agentadaptor.Skill, len(skills))
	for k, skill := range skills {
		if pin, ok := lookup[skill.Key]; ok {
			skill.Required = true
			if pin.Reason != "" {
				skill.Reason = pin.Reason
			}
		}
		out[k] = skill
	}
	return out, nil
}

// markRequiredCatalog extends markRequiredProvider with the Catalogue
// method so SkillCatalog-shaped wrappers stay catalog-shaped through
// the decorator chain.
type markRequiredCatalog struct {
	*markRequiredProvider
}

// Catalogue implements SkillCatalog.
//
// Identical pin-application semantics as GetSkills: every entry in
// the upstream catalogue is examined, matching entries are promoted
// to Required.
func (p *markRequiredCatalog) Catalogue(ctx context.Context) ([]agentadaptor.Skill, error) {
	if p.inner == nil {
		return nil, nil
	}
	cat, ok := p.inner.(agentadaptor.SkillCatalog)
	if !ok {
		// Defensive — constructor should have routed this case to the
		// non-catalog branch.
		return nil, nil
	}
	skills, err := cat.Catalogue(ctx)
	if err != nil {
		return nil, err
	}
	if len(p.pins) == 0 {
		return skills, nil
	}
	lookup := pinLookup(p.pins)
	out := make([]agentadaptor.Skill, len(skills))
	for i, skill := range skills {
		if pin, ok := lookup[skill.Key]; ok {
			skill.Required = true
			if pin.Reason != "" {
				skill.Reason = pin.Reason
			}
		}
		out[i] = skill
	}
	return out, nil
}

func pinLookup(pins []Pin) map[string]Pin {
	out := make(map[string]Pin, len(pins))
	for _, pin := range pins {
		out[pin.Key] = pin
	}
	return out
}
