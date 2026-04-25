package agentadaptor

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// resolveSkills merges all skill candidates for one Run / Admin call,
// materialises any non-path sources through the SkillMaterializer, and
// returns the adapter-facing ResolvedSkills together with the selected
// keys and the full merged catalogue.
//
// Sources are combined additively (v0.5 SkillProvider model):
//   - inline Skill values from defaultRefs / runRefs / candidateRefs
//     are taken at face value.
//   - all bare SkillKey refs are batched into a single call to
//     SkillProvider.GetSkills(ctx, keys). The returned map provides
//     the Skill description for each key. Keys absent from the
//     returned map surface as ErrSkillNotFound.
//   - the provider MAY also return Skill values whose key is not in
//     the supplied keys list — these are tenant-mandatory ("required")
//     entries that the SDK adds to the selected set automatically.
//
// Duplicate keys must be structurally equal; conflicting duplicates
// return *SkillKeyConflictError (wrapping ErrSkillKeyConflict).
//
// AgentIdentity is propagated to the provider through ctx via
// CallerIdentityFromContext so multi-tenant providers can scope their
// lookup without forcing every caller to carry tenant in the public
// signature.
func (s *sdkImpl) resolveSkills(
	ctx context.Context,
	identity AgentIdentity,
	defaultRefs []SkillRef,
	runRefs []SkillRef,
	candidateRefs []SkillRef,
) (payload ResolvedSkills, selected []string, resolved []Skill, err error) {
	state := newResolutionState()

	// 1. Inline candidates registered through binding-only candidates
	//    (used by Admin.SetSelectedSkills to expose inline Skill values
	//    coming from WithDefaultSkills without forcing them into
	//    selection by themselves).
	for _, ref := range candidateRefs {
		if skill, ok := ref.(Skill); ok {
			if err := state.merger.add(sourceLabelCandidate, skill); err != nil {
				return ResolvedSkills{}, nil, nil, err
			}
		}
	}

	// 2. Inline Skill values go directly into the merger; bare
	//    SkillKey refs are collected so we can (a) batch them into a
	//    single provider call below and (b) replay them as merger
	//    source labels after the provider has populated the entries
	//    (so user selection — even by bare key — is reflected in the
	//    final Selected set).
	if err := state.absorbRefs(sourceLabelDefault, defaultRefs); err != nil {
		return ResolvedSkills{}, nil, nil, err
	}
	if err := state.absorbRefs(sourceLabelRun, runRefs); err != nil {
		return ResolvedSkills{}, nil, nil, err
	}

	// 3. Ask the provider for the Skill descriptions of every bare
	//    SkillKey referenced. The provider may also return tenant-
	//    mandatory skills not in the keys list; we treat them as
	//    additional members of the selected set.
	providerKeys := state.requestedKeys.values()
	providerSkills, err := s.fetchSkillsFromProvider(ctx, identity, providerKeys)
	if err != nil {
		return ResolvedSkills{}, nil, nil, err
	}

	// 3a. Inject every Skill the provider returned into the merger.
	//     Conflicts with inline values surface as ErrSkillKeyConflict.
	for _, skill := range providerSkills {
		if strings.TrimSpace(skill.Key) == "" {
			continue
		}
		if err := state.merger.add(sourceLabelProvider, skill); err != nil {
			return ResolvedSkills{}, nil, nil, err
		}
	}

	// 3b. Validate that every user-requested key was either provided
	//     inline (already in the merger before the provider call) or
	//     returned by the provider.
	for _, key := range providerKeys {
		if !state.merger.has(key) {
			return ResolvedSkills{}, nil, nil, fmt.Errorf("%w: key %q was requested via WithDefaultSkills/WithSkills but the configured SkillProvider did not return it", ErrSkillNotFound, key)
		}
	}

	// 3c. Replay bare SkillKey references against the now-populated
	//     merger so the originating sourceLabel (default / run) is
	//     attached to the entry. Without this, a SkillKey ref that
	//     resolved against a candidate-only entry would never count as
	//     selected even though the user explicitly asked for it.
	for _, ref := range state.keyRefs {
		if err := state.merger.addKey(ref.label, ref.key); err != nil {
			return ResolvedSkills{}, nil, nil, err
		}
	}

	// 4. Build the final Selected list and materialise in a single
	//    pass. Selected = provider ∪ default ∪ run; candidate-only
	//    entries are intentionally skipped (they are registered for
	//    Admin.SetSelectedSkills lookups, not auto-selection).
	selectedSet := map[string]struct{}{}
	selectedList := make([]string, 0)
	mergedByKey := map[string]Skill{}
	mergedSkills := state.merger.skills()
	for _, skill := range mergedSkills {
		mergedByKey[skill.Key] = skill
		if state.merger.hasSource(skill.Key, sourceLabelProvider) ||
			state.merger.hasSource(skill.Key, sourceLabelDefault) ||
			state.merger.hasSource(skill.Key, sourceLabelRun) {
			if _, dup := selectedSet[skill.Key]; !dup && skill.Key != "" {
				selectedSet[skill.Key] = struct{}{}
				selectedList = append(selectedList, skill.Key)
			}
		}
	}
	sort.Strings(selectedList)

	// 5. Materialise each Selected skill. Failures degrade to a
	//    warning so the rest of the run / snapshot can proceed; the
	//    adapter is responsible for rendering the missing state.
	materializer := s.skillMaterializer
	if materializer == nil {
		materializer = newDefaultSkillMaterializer()
	}
	entries := make([]ResolvedSkill, 0, len(selectedList))
	warnings := make([]string, 0)
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
		Fingerprint: stableHash(identity.TenantID, identity.ProfileID, selectedList, entries, warnings),
	}
	return payload, selectedList, mergedSkills, nil
}

// refLabel records a (SkillKey, sourceLabel) pair that the resolution
// layer replays against the merger after the provider has populated
// every entry. See step 3c in resolveSkills.
type refLabel struct {
	key   string
	label sourceLabel
}

// resolutionState is the per-call working set the resolveSkills loop
// passes around. Keeping merger / requestedKeys / keyRefs in one
// struct keeps the helper signatures narrow (one in/out parameter)
// and makes "what the loop accumulates" explicit.
type resolutionState struct {
	merger        *skillMerger
	requestedKeys orderedKeySet
	keyRefs       []refLabel
}

func newResolutionState() *resolutionState {
	return &resolutionState{merger: newSkillMerger()}
}

// absorbRefs adds every inline Skill in refs to state.merger and
// records every SkillKey in state.requestedKeys (plus appends to
// state.keyRefs so step 3c can replay the reference once the
// provider has populated entries). Unsupported ref types return an
// error.
func (st *resolutionState) absorbRefs(label sourceLabel, refs []SkillRef) error {
	for _, ref := range refs {
		switch value := ref.(type) {
		case nil:
			continue
		case SkillKey:
			key := normalizeSkillKey(string(value))
			if key == "" {
				continue
			}
			st.requestedKeys.add(key)
			st.keyRefs = append(st.keyRefs, refLabel{key: key, label: label})
		case Skill:
			if err := st.merger.add(label, value); err != nil {
				return err
			}
		default:
			return fmt.Errorf("agentadaptor: unsupported SkillRef type %T", ref)
		}
	}
	return nil
}

// fetchSkillsFromProvider invokes the configured SkillProvider with
// the requested keys, propagating the AgentIdentity through ctx so
// providers that need scoping can read it via CallerIdentityFromContext.
//
// A nil / unset provider returns nil; the resolution layer then falls
// back to inline Skill values exclusively.
func (s *sdkImpl) fetchSkillsFromProvider(ctx context.Context, identity AgentIdentity, keys []string) (map[string]Skill, error) {
	provider := s.skillProvider
	if provider == nil {
		return nil, nil
	}
	scoped := WithCallerIdentity(ctx, identity)
	skills, err := provider.GetSkills(scoped, keys)
	if err != nil {
		return nil, err
	}
	return skills, nil
}

// collectAdminCandidates returns the candidate pool admin paths
// (ListSkills / SetSelectedSkills) feed into resolveSkills as the
// non-selected catalogue.
//
// Pool composition:
//
//   - binding-inline skills (defaults.Skills) — visible candidates
//     even when the operator is overriding the selection
//   - upstream SkillCatalog.Catalogue() entries when the configured
//     SkillProvider implements SkillCatalog — these expose the full
//     enumerable catalogue so admin can render "available but off"
//     rows. Providers that don't implement SkillCatalog (e.g. remote
//     stores) contribute nothing here, which leaves the snapshot in
//     the SkillSyncUnsupported mode the host UI should detect and
//     route to the store's own discovery surface.
//
// Errors from Catalogue propagate verbatim. The returned slice is
// safe to append to without disturbing defaults.
func (s *sdkImpl) collectAdminCandidates(ctx context.Context, defaults AgentDefaults) ([]SkillRef, error) {
	candidates := append([]SkillRef(nil), defaults.Skills...)
	if cat, ok := s.skillProvider.(SkillCatalog); ok {
		scoped := WithCallerIdentity(ctx, defaults.Agent)
		catalogue, err := cat.Catalogue(scoped)
		if err != nil {
			return nil, err
		}
		for _, skill := range catalogue {
			candidates = append(candidates, skill)
		}
	}
	return candidates, nil
}

// orderedKeySet is a small insertion-ordered set of normalised
// SkillKey strings. Insertion order matters because user-facing error
// messages report the first missing key.
type orderedKeySet struct {
	seen  map[string]struct{}
	order []string
}

func (o *orderedKeySet) add(key string) {
	if o.seen == nil {
		o.seen = map[string]struct{}{}
	}
	if _, ok := o.seen[key]; ok {
		return
	}
	o.seen[key] = struct{}{}
	o.order = append(o.order, key)
}

func (o *orderedKeySet) values() []string {
	return o.order
}
