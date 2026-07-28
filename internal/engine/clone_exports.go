package engine

// Exported operations used by the public Agent pipeline.

// StableHash exposes stableHash for the root package.
func StableHash(parts ...any) string { return stableHash(parts...) }

// CloneRuntimeServiceSpecs exposes cloneRuntimeServiceSpecs for the root package.
func CloneRuntimeServiceSpecs(values []RuntimeServiceSpec) []RuntimeServiceSpec {
	return cloneRuntimeServiceSpecs(values)
}

// CloneRuntimeServiceRefs exposes cloneRuntimeServiceRefs for the root package.
func CloneRuntimeServiceRefs(values []RuntimeServiceRef) []RuntimeServiceRef {
	return cloneRuntimeServiceRefs(values)
}

// CloneConfigSchema exposes cloneConfigSchema for the root package.
func CloneConfigSchema(schema *ConfigSchema) *ConfigSchema { return cloneConfigSchema(schema) }

// CloneEnvBindings exposes cloneEnvBindings for the root package.
func CloneEnvBindings(values []EnvBinding) []EnvBinding { return cloneEnvBindings(values) }
