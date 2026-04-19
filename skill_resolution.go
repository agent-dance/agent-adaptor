package agentadaptor

import "context"

func (s *sdkImpl) prepareSkills(
	ctx context.Context,
	binding AgentBinding,
	agent AgentIdentity,
	workspace WorkspaceLease,
	requested []string,
) (SkillPayload, error) {
	driverType := binding.Adapter().Descriptor().Type
	available := []Skill(nil)
	if inventory, ok := s.skillCatalog.(SkillCatalogInventory); ok {
		var err error
		available, err = inventory.List(ctx, agent.TenantID)
		if err != nil {
			return SkillPayload{}, err
		}
	}

	resolveRefs := cloneStrings(requested)
	if len(available) > 0 {
		resolveRefs = make([]string, 0, len(requested))
		for _, ref := range requested {
			if canonical := canonicalSkillRef(ref, available); canonical != "" {
				resolveRefs = append(resolveRefs, canonical)
			}
		}
		resolveRefs = mergeUniqueStrings(resolveRefs, skillKeys(requiredSkills(available)))
	}

	resolved, err := s.skillCatalog.Resolve(ctx, agent.TenantID, resolveRefs)
	if err != nil {
		return SkillPayload{}, err
	}

	return s.skillAssembler.Prepare(ctx, SkillAssemblyRequest{
		DriverType: driverType,
		TenantID:   agent.TenantID,
		Agent:      agent,
		Config:     binding.Config(),
		Workspace:  workspace,
		Requested:  cloneStrings(requested),
		Available:  cloneSkills(available),
		Resolved:   cloneSkills(resolved),
	})
}
