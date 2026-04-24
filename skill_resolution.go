package agentadaptor

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// resolveSkills merges all skill candidates for one Run / Admin call,
// materialises any non-path sources through the SkillMaterializer, and
// returns the adapter-facing ResolvedSkills together with the selected
// keys and the full merged catalogue.
//
// Sources are combined additively:
//   - Provider.List(ctx, tenantID): authoritative catalogue + Required skills.
//   - candidateRefs:                binding-only candidates that are registered
//     as available (so bare keys can resolve) but NOT selected. Used by
//     Admin.SetSelectedSkills to expose inline Skill values coming from
//     WithDefaultSkills without forcing them into the selection.
//   - defaultRefs:                  binding WithDefaultSkills (or the admin
//     SetSelectedSkills override rewritten into SkillKey refs).
//   - runRefs:                      per-run WithSkills.
//
// Duplicate keys must be structurally equal; conflicting duplicates return
// *SkillKeyConflictError (wrapping ErrSkillKeyConflict).
func (s *sdkImpl) resolveSkills(
	ctx context.Context,
	tenantID string,
	defaultRefs []SkillRef,
	runRefs []SkillRef,
	candidateRefs []SkillRef,
) (payload ResolvedSkills, selected []string, resolved []Skill, err error) {
	providerSkills, providerOK, providerErr := s.enumerateProvider(ctx, tenantID)
	if providerErr != nil {
		return ResolvedSkills{}, nil, nil, providerErr
	}

	merger := newSkillMerger()

	// 1. Provider-supplied skills are the authoritative catalogue. Only
	//    Required entries become implicit additions to the Selected set; all
	//    other provider entries remain in the candidate pool so that
	//    SkillKey references from WithDefaultSkills / WithSkills can resolve.
	for _, skill := range providerSkills {
		if err := merger.add(sourceLabelProvider, skill); err != nil {
			return ResolvedSkills{}, nil, nil, err
		}
	}

	// 2. Register binding-only candidates. These become visible in the
	//    merger so bare SkillKey refs from defaultRefs / runRefs can resolve
	//    against them, but they do not contribute to the Selected set by
	//    themselves. Inline Skill values participate directly; bare keys in
	//    this slot are ignored because they only make sense for selection.
	for _, ref := range candidateRefs {
		if skill, ok := ref.(Skill); ok {
			if err := merger.add(sourceLabelCandidate, skill); err != nil {
				return ResolvedSkills{}, nil, nil, err
			}
		}
	}

	// 3. Apply binding defaults / admin override.
	if err := s.applyRefs(merger, sourceLabelDefault, defaultRefs, providerOK); err != nil {
		return ResolvedSkills{}, nil, nil, err
	}

	// 4. Apply per-run refs.
	if err := s.applyRefs(merger, sourceLabelRun, runRefs, providerOK); err != nil {
		return ResolvedSkills{}, nil, nil, err
	}

	// 5. Determine the final Selected set: provider Required skills + all
	//    skills whose key was explicitly selected via default / run.
	//
	//    NOTE: the Selected list is intentionally decoupled from the
	//    Entries list (step 6). A skill can legitimately appear in
	//    Selected without producing a payload entry when materialisation
	//    fails; adapters surface that as SkillStateMissing in the
	//    snapshot. Keeping both lists lets the Admin snapshot distinguish
	//    "user asked for X" from "X is ready on disk".
	selectedSet := map[string]struct{}{}
	selectedList := make([]string, 0)
	for _, skill := range merger.skills() {
		if skill.Required || merger.selected(skill.Key) {
			if _, ok := selectedSet[skill.Key]; ok {
				continue
			}
			selectedSet[skill.Key] = struct{}{}
			selectedList = append(selectedList, skill.Key)
		}
	}
	sort.Strings(selectedList)

	// 6. Materialise each Selected skill. Failures degrade gracefully to
	//    a warning so the rest of the run / snapshot can proceed; the
	//    adapter is responsible for rendering the missing state.
	materializer := s.skillMaterializer
	if materializer == nil {
		materializer = newDefaultSkillMaterializer()
	}
	entries := make([]ResolvedSkill, 0, len(selectedList))
	warnings := make([]string, 0)
	mergedByKey := map[string]Skill{}
	for _, skill := range merger.skills() {
		mergedByKey[skill.Key] = skill
	}
	for _, key := range selectedList {
		skill := mergedByKey[key]
		sourcePath, matErr := materializer.Materialize(ctx, skill)
		if matErr != nil {
			warnings = append(warnings, fmt.Sprintf("skill %q materialization failed: %v", key, matErr))
			continue
		}
		entries = append(entries, ResolvedSkill{
			Key:         skill.Key,
			RuntimeName: defaultSkillRuntimeName(skill),
			SourcePath:  sourcePath,
			Required:    skill.Required,
			Reason:      skill.Reason,
			Metadata:    cloneStringMap(skill.Metadata),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })

	mode := SkillSyncEphemeral
	if len(entries) == 0 && len(selectedList) == 0 {
		mode = SkillSyncUnsupported
	}

	payload = ResolvedSkills{
		Mode:        mode,
		Entries:     entries,
		Warnings:    warnings,
		Fingerprint: stableHash(tenantID, selectedList, entries, warnings),
	}
	return payload, selectedList, merger.skills(), nil
}

// applyRefs records each SkillRef in refs into merger under the given
// source label. Inline Skill values are inserted directly. Bare SkillKey
// refs must already exist in the merger; otherwise the error is tailored
// to the provider's state:
//
//   - providerOK=false (provider exists but refused enumeration): the SDK
//     has no way to verify the key, so we return ErrSkillsNotEnumerable
//     with a hint to pass a full Skill value instead.
//   - providerOK=true  (no provider, or provider returned an empty list,
//     or candidate Skills did not cover the key): the key truly cannot be
//     resolved anywhere, so we return ErrSkillNotFound.
func (s *sdkImpl) applyRefs(merger *skillMerger, label sourceLabel, refs []SkillRef, providerOK bool) error {
	for _, ref := range refs {
		switch value := ref.(type) {
		case nil:
			continue
		case SkillKey:
			key := normalizeSkillKey(string(value))
			if key == "" {
				continue
			}
			if !merger.has(key) {
				if !providerOK {
					return fmt.Errorf("%w: cannot resolve bare key %q because the SkillProvider is not enumerable; pass a full Skill value instead", ErrSkillsNotEnumerable, key)
				}
				return fmt.Errorf("%w: key %q referenced by %s is not available from any configured SkillProvider, WithDefaultSkills, or WithSkills value", ErrSkillNotFound, key, label)
			}
			if err := merger.addKey(label, key); err != nil {
				return err
			}
		case Skill:
			if err := merger.add(label, value); err != nil {
				return err
			}
		default:
			return fmt.Errorf("agentadaptor: unsupported SkillRef type %T", ref)
		}
	}
	return nil
}

// enumerateProvider calls s.skillProvider.List and returns the skills plus a
// flag indicating whether enumeration succeeded. ErrSkillsNotEnumerable is
// treated as a graceful degradation: the caller can still construct a
// ResolvedSkills payload from inline Skill values alone.
func (s *sdkImpl) enumerateProvider(ctx context.Context, tenantID string) ([]Skill, bool, error) {
	provider := s.skillProvider
	if provider == nil {
		return nil, true, nil
	}
	skills, err := provider.List(ctx, tenantID)
	if err != nil {
		if errors.Is(err, ErrSkillsNotEnumerable) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return skills, true, nil
}

