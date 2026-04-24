// Package providers hosts small, composable decorators that adapt one
// SkillProvider value into another one. These decorators intentionally live
// outside the main SDK surface so they remain opt-in sugar rather than
// first-class API.
//
// The canonical use case is "the upstream provider is owned by someone else
// and I cannot modify it, but in my deployment skill X must be Required".
// Hosts typically do this at the SkillProvider level (tenant-aware catalog
// returning Required=true from List) — this package covers the degenerate
// case where that is not possible.
package providers

import (
	"context"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// Pin describes one (Key, Reason) pair the MarkRequired decorator should
// project onto the upstream provider's catalogue.
type Pin struct {
	// Key is the Skill.Key the decorator will match against the upstream
	// provider's List() result.
	Key string
	// Reason is copied onto Skill.Reason when Required is set to true by
	// the decorator. An empty Reason means the decorator leaves the existing
	// Reason alone (so the upstream one wins if present).
	Reason string
}

// MarkRequired wraps inner so that, after every List call, the skills whose
// Key matches one of the supplied Pin entries have their Required flag set
// to true (and Reason filled if Pin.Reason is non-empty).
//
// The wrapping is intentionally additive:
//
//   - Skills outside the pin set are returned verbatim.
//   - Pins that do not match any skill in the upstream catalogue are silently
//     ignored. Hosts that want hard validation can compare len(result) vs
//     len(pins) themselves.
//   - If inner is nil, List returns (nil, nil) and the pins are effectively
//     dormant. MarkRequired is a decorator, not a source: it promotes
//     existing skills to Required but never synthesises skill bodies.
//     Callers who want to inject brand-new required skills should register
//     them through binding defaults (WithDefaultSkills) or an inline
//     provider instead.
//
// The decorator forwards ErrSkillsNotEnumerable (and any other error)
// unchanged. In that case the pins have nothing to attach to, so the caller
// must handle the degraded enumerate-less path themselves — for example by
// declaring the required skills inline through the binding defaults.
func MarkRequired(inner agentadaptor.SkillProvider, pins ...Pin) agentadaptor.SkillProvider {
	cleaned := make([]Pin, 0, len(pins))
	for _, pin := range pins {
		if pin.Key == "" {
			continue
		}
		cleaned = append(cleaned, pin)
	}
	return &markRequiredProvider{inner: inner, pins: cleaned}
}

type markRequiredProvider struct {
	inner agentadaptor.SkillProvider
	pins  []Pin
}

// List implements SkillProvider.
//
// The merge algorithm walks the upstream catalogue once, promoting matching
// skills in-place. Non-matching pins are ignored. The resulting slice is a
// fresh allocation so callers are free to mutate it without disturbing the
// upstream provider's internal state.
func (p *markRequiredProvider) List(ctx context.Context, tenantID string) ([]agentadaptor.Skill, error) {
	if p.inner == nil {
		return nil, nil
	}
	skills, err := p.inner.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if len(p.pins) == 0 {
		return skills, nil
	}
	lookup := make(map[string]Pin, len(p.pins))
	for _, pin := range p.pins {
		lookup[pin.Key] = pin
	}
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
