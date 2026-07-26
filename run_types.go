package agentadaptor

// resolvedInvocation is the fully merged view of one Run/Start call. It stays
// in the root package only until the runner core moves into internal/engine
// (P0.2 batch 3); RunResult and the host-hook manager interfaces already live
// in engine and are re-exported from engine_aliases.go.
type resolvedInvocation struct {
	runID          string
	prompt         string
	adapter        DriverAdapter
	config         any
	agent          AgentIdentity
	workspace      WorkspaceLease
	runtime        RuntimePayload
	skills         ResolvedSkills
	mcp            MCPPayload
	profilePayload ProfilePayload
	profile        *ProfileSelection
	policy         RunPolicy
	handlers       decisionHandlers
	instructions   *InstructionsBundleRef
	session        SessionRequest
	metadata       map[string]string
	outputSchema   *OutputSchema
	outputSource   StructuredOutputSource
	fingerprint    string
	streaming      bool
	model          string
}
