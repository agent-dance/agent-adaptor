package engine

import (
	"context"
)

// prepareRuntimePayload resolves an already-merged desired service set and a
// partially filled request envelope,
// it produces the driver-facing RuntimePayload (requested clone, fingerprint,
// manager Ensure, secret-env collection, ref normalization).
// req.Desired is filled here so callers cannot disagree with
// payload.Requested; a nil manager behaves like the noop manager.
func prepareRuntimePayload(
	ctx context.Context,
	manager RuntimeServiceManager,
	req RuntimeServiceRequest,
	desired []RuntimeServiceSpec,
) (RuntimePayload, error) {
	payload := RuntimePayload{Requested: cloneRuntimeServiceSpecs(desired)}
	payload.Fingerprint = stableHash(req.DriverType, payload.Requested)

	if len(payload.Requested) == 0 {
		return payload, nil
	}
	if manager == nil {
		manager = noopRuntimeManager{}
	}

	req.Desired = cloneRuntimeServiceSpecs(payload.Requested)
	ensured, err := manager.Ensure(ctx, req)
	if err != nil {
		return RuntimePayload{}, err
	}
	payload.SecretEnv = collectRuntimeSecretEnv(ensured)
	payload.Ensured = normalizeRuntimeServiceRefs(payload.Requested, ensured, req.Agent)
	return payload, nil
}

func collectRuntimeSecretEnv(refs []RuntimeServiceRef) []EnvBinding {
	if len(refs) == 0 {
		return nil
	}
	var out []EnvBinding
	for _, ref := range refs {
		out = append(out, cloneEnvBindings(ref.SecretEnv)...)
	}
	return out
}

func runtimeReportsFromRefs(refs []RuntimeServiceRef, owner AgentIdentity) []RuntimeServiceReport {
	if len(refs) == 0 {
		return nil
	}
	out := make([]RuntimeServiceReport, 0, len(refs))
	for _, ref := range refs {
		status := ref.Status
		if status == "" {
			status = RuntimeServiceRunning
		}
		lifecycle := ref.Lifecycle
		if lifecycle == "" {
			lifecycle = RuntimeLifecycleShared
		}
		ownerID := ref.OwnerAgentID
		if ownerID == "" {
			ownerID = owner.ID
		}
		health := ref.Health
		if health == "" {
			health = RuntimeHealthUnknown
		}
		out = append(out, RuntimeServiceReport{
			ID:           ref.ID,
			Name:         ref.Name,
			URL:          ref.URL,
			Status:       status,
			Lifecycle:    lifecycle,
			ReuseKey:     ref.ReuseKey,
			Command:      ref.Command,
			CWD:          ref.CWD,
			Port:         ref.Port,
			OwnerAgentID: ownerID,
			Health:       health,
			Metadata:     cloneStringMap(ref.Metadata),
		})
	}
	return out
}

func normalizeRuntimeServiceRefs(requested []RuntimeServiceSpec, ensured []RuntimeServiceRef, owner AgentIdentity) []RuntimeServiceRef {
	if len(ensured) == 0 {
		return nil
	}
	requestedByKey := map[string]RuntimeServiceSpec{}
	for _, spec := range requested {
		for _, key := range runtimeServiceLookupKeys(spec.ID, spec.Name) {
			requestedByKey[key] = spec
		}
	}
	out := make([]RuntimeServiceRef, 0, len(ensured))
	for _, ref := range ensured {
		normalized := ref
		if spec, ok := lookupRuntimeServiceSpec(requestedByKey, ref.ID, ref.Name); ok {
			if normalized.ID == "" {
				normalized.ID = spec.ID
			}
			if normalized.Name == "" {
				normalized.Name = spec.Name
			}
			if normalized.URL == "" {
				normalized.URL = spec.URL
			}
			if normalized.Lifecycle == "" {
				normalized.Lifecycle = spec.Lifecycle
			}
			if normalized.ReuseKey == "" {
				normalized.ReuseKey = spec.ReuseKey
			}
			if normalized.Command == "" {
				normalized.Command = spec.Command
			}
			if normalized.CWD == "" {
				normalized.CWD = spec.CWD
			}
			if normalized.Port == 0 {
				normalized.Port = spec.Port
			}
			normalized.Metadata = mergeStringMaps(spec.Metadata, normalized.Metadata)
		}
		if normalized.Status == "" {
			normalized.Status = RuntimeServiceRunning
		}
		if normalized.Lifecycle == "" {
			normalized.Lifecycle = RuntimeLifecycleShared
		}
		if normalized.OwnerAgentID == "" {
			normalized.OwnerAgentID = owner.ID
		}
		if normalized.Health == "" {
			normalized.Health = RuntimeHealthUnknown
		}
		out = append(out, normalized)
	}
	return cloneRuntimeServiceRefs(out)
}

func lookupRuntimeServiceSpec(index map[string]RuntimeServiceSpec, id, name string) (RuntimeServiceSpec, bool) {
	for _, key := range runtimeServiceLookupKeys(id, name) {
		if spec, ok := index[key]; ok {
			return spec, true
		}
	}
	return RuntimeServiceSpec{}, false
}

func runtimeServiceLookupKeys(id, name string) []string {
	keys := make([]string, 0, 2)
	if id != "" {
		keys = append(keys, "id:"+id)
	}
	if name != "" {
		keys = append(keys, "name:"+name)
	}
	return keys
}
