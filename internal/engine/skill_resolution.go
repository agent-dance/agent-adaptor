package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/skill"
)

// resolveSkillsWith merges all skill candidates for one run or inspection,
// materialises any non-path sources through the SkillMaterializer, and
// returns the Driver-facing ResolvedSkills together with the selected
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
func resolveSkillsWith(
	ctx context.Context,
	provider SkillProvider,
	skillMaterializer SkillMaterializer,
	identity AgentIdentity,
	defaultRefs []SkillRef,
	runRefs []SkillRef,
	candidateRefs []SkillRef,
) (payload ResolvedSkills, selected []string, resolved []Skill, err error) {
	state := newResolutionState()

	// 1. Inline candidates registered for Agent.SelectSkills without forcing
	//    them into the selection by themselves.
	// collectSkillCandidatesFrom places the Agent defaults first. Those
	// values are registration copies, not independent declarations; consume
	// one matching candidate for each inline default before conflict-aware
	// merging. Consuming those registration copies prevents a candidate/default
	// self-join without inventing equality for Go function values.
	defaultCandidateCopies := make(map[string]int)
	for _, ref := range defaultRefs {
		if value, ok := ref.(Skill); ok {
			defaultCandidateCopies[normalizeSkillKey(value.Key)]++
		}
	}
	for _, ref := range candidateRefs {
		if skill, ok := ref.(Skill); ok {
			key := normalizeSkillKey(skill.Key)
			if defaultCandidateCopies[key] > 0 {
				defaultCandidateCopies[key]--
				continue
			}
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
	providerSkills, err := fetchSkillsFrom(ctx, provider, identity, providerKeys)
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
			return ResolvedSkills{}, nil, nil, fmt.Errorf("%w: key %q was requested via Agent defaults or WithSkills but the configured SkillProvider did not return it", ErrSkillNotFound, key)
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
	//    Agent.SelectSkills lookups, not auto-selection).
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

	// 5. Materialise each Selected skill. A selected skill is part of the
	//    run contract, so materialization failure is fatal and must surface
	//    before the adapter starts.
	materializer := skillMaterializer
	if materializer == nil {
		materializer = skill.NewDefaultSkillMaterializer()
	}
	entries := make([]ResolvedSkill, 0, len(selectedList))
	var warnings []string
	for _, key := range selectedList {
		skill := mergedByKey[key]
		runtimeName := defaultSkillRuntimeName(skill)
		sourcePath, matErr := materializer.Materialize(ctx, skill)
		if matErr != nil {
			return ResolvedSkills{}, selectedList, mergedSkills, &SkillMaterializationError{
				Key:         key,
				RuntimeName: runtimeName,
				Cause:       matErr,
			}
		}
		entries = append(entries, ResolvedSkill{
			Key:         skill.Key,
			RuntimeName: runtimeName,
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

// fetchSkillsFrom invokes the given SkillProvider with the requested keys,
// propagating the AgentIdentity through ctx so providers that need scoping
// can read it via CallerIdentityFromContext.
//
// A nil / unset provider returns nil; the resolution layer then falls
// back to inline Skill values exclusively.
func fetchSkillsFrom(ctx context.Context, provider SkillProvider, identity AgentIdentity, keys []string) (map[string]Skill, error) {
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

// collectSkillCandidatesFrom returns the candidate pool inspection paths
// (Skills / SelectSkills) feed into resolveSkills as the
// non-selected catalogue.
//
// Pool composition (and order, which is part of the internal contract):
//
//   - Agent-default inline skills first — visible candidates
//     even when the operator is overriding the selection
//   - upstream SkillCatalog.Catalogue() entries when the configured
//     SkillProvider implements SkillCatalog — these expose the full
//     enumerable catalogue so an inspector can render "available but off"
//     rows. Providers that don't implement SkillCatalog (e.g. remote
//     stores) contribute nothing here, which leaves the snapshot in
//     the SkillSyncUnsupported mode the host UI should detect and
//     route to the store's own discovery surface.
//
// Errors from Catalogue propagate verbatim. The returned slice is
// safe to append to without disturbing defaults.
func collectSkillCandidatesFrom(ctx context.Context, provider SkillProvider, identity AgentIdentity, defaultSkills []SkillRef) ([]SkillRef, error) {
	candidates := append([]SkillRef(nil), defaultSkills...)
	if cat, ok := provider.(SkillCatalog); ok {
		scoped := WithCallerIdentity(ctx, identity)
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
