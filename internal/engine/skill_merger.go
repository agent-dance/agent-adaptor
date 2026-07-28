package engine

import "fmt"

// Conflict-aware skill merging for provider, catalog, Agent-default, and
// call-scoped candidates.

type sourceLabel string

const (
	sourceLabelProvider  sourceLabel = "provider"
	sourceLabelCandidate sourceLabel = "agent:candidate"
	sourceLabelDefault   sourceLabel = "agent:default"
	sourceLabelRun       sourceLabel = "run:per-call"
)

type skillBucket struct {
	skill   Skill
	sources map[sourceLabel]struct{}
}

// skillMerger accumulates Skill candidates from multiple sources and
// enforces the "same key, same value" invariant.
type skillMerger struct {
	entries map[string]*skillBucket
	order   []string
}

func newSkillMerger() *skillMerger {
	return &skillMerger{entries: map[string]*skillBucket{}}
}

// add inserts a skill under the given sourceLabel. If a different skill has
// previously been recorded under the same key, a *SkillKeyConflictError is
// returned.
func (m *skillMerger) add(label sourceLabel, s Skill) error {
	key := normalizeSkillKey(s.Key)
	if key == "" {
		return fmt.Errorf("%w: source %q", ErrSkillKeyMissing, label)
	}
	if s.Source == nil {
		return fmt.Errorf("%w: skill %q from source %q", ErrSkillSourceMissing, key, label)
	}
	existing, ok := m.entries[key]
	if !ok {
		m.entries[key] = &skillBucket{skill: cloneSkill(s), sources: map[sourceLabel]struct{}{label: {}}}
		m.order = append(m.order, key)
		return nil
	}
	if !skillsEquivalent(existing.skill, s) {
		labels := []string{string(label)}
		for existingLabel := range existing.sources {
			labels = append(labels, string(existingLabel))
		}
		return &SkillKeyConflictError{
			Key:     key,
			Sources: labels,
			Detail:  skillDiffDetail(existing.skill, s),
		}
	}
	existing.sources[label] = struct{}{}
	return nil
}

// addKey records a bare SkillKey reference. It requires that the key is
// already present in the merger (via the provider enumeration). Missing keys
// produce ErrSkillNotFound, which surfaces as a user-facing "you passed a
// key that the provider does not recognise" message.
func (m *skillMerger) addKey(label sourceLabel, key string) error {
	key = normalizeSkillKey(key)
	if key == "" {
		return fmt.Errorf("%w: source %q", ErrSkillKeyMissing, label)
	}
	existing, ok := m.entries[key]
	if !ok {
		return fmt.Errorf("%w: key %q referenced by %s", ErrSkillNotFound, key, label)
	}
	existing.sources[label] = struct{}{}
	return nil
}

// selected returns the union of source labels for a given key. Used to tell
// downstream code whether the skill was explicitly selected (default or
// per-run) or merely visible via the provider.
func (m *skillMerger) selected(key string) bool {
	bucket, ok := m.entries[key]
	if !ok {
		return false
	}
	if _, defaulted := bucket.sources[sourceLabelDefault]; defaulted {
		return true
	}
	if _, perRun := bucket.sources[sourceLabelRun]; perRun {
		return true
	}
	return false
}

// has reports whether a key is known to the merger.
func (m *skillMerger) has(key string) bool {
	_, ok := m.entries[normalizeSkillKey(key)]
	return ok
}

// hasSource reports whether the given source label has contributed to
// the bucket for key. Used by the resolution layer to decide which
// merged entries belong to the run's selected set (anything from
// provider / default / run is selected; candidate-only entries are
// registered for SelectSkills lookups but not auto-selected).
func (m *skillMerger) hasSource(key string, label sourceLabel) bool {
	bucket, ok := m.entries[normalizeSkillKey(key)]
	if !ok {
		return false
	}
	_, present := bucket.sources[label]
	return present
}

// skills returns the merged skills in insertion order.
func (m *skillMerger) skills() []Skill {
	out := make([]Skill, 0, len(m.order))
	for _, key := range m.order {
		out = append(out, cloneSkill(m.entries[key].skill))
	}
	return out
}
