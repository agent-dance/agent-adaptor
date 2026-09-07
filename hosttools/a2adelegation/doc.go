// Package a2adelegation provides optional host-curated Local and Remote
// delegation through the public Runner and A2A contracts.
//
// Service.Option binds one authorized publisher per leader run. Accepted
// CapabilityInvocation, TodoUpdated, and SubagentUpdate events reach core's
// unique sink and bounded observers before the component EventBus. The bus is
// a lossy UI/replay view; Config.Observe does not provide recording guarantees.
// EventBus.RunEventsBound is historical proof of a successful binding, not
// authority to publish after run teardown.
//
// Each Delegate owns a fresh active budget. The private activebudget package
// is used only as a neutral clock/controller implementation: delegation neither
// enters internal/engine nor dispatches a Driver, and no internal type appears
// in its public API. Local Send and SendStream each execute one Runner.Stream;
// a qualified unique last RunFinished may classify an existing bare error only
// after complete drain. Error alone decides failure and RunError always wins.
// Member Policy and the existing wall-clock Timeout retain
// their meanings. See Delegator.Delegate for the bounded lifecycle contract.
//
// Relayed identities use reversible aa1 tuple domains. The parent sink assigns
// the authoritative run sequence; the upstream coordinates and recursive
// provenance remain in EventMeta.Source. An expanded identity or source chain
// that cannot fit the public contract produces an explicit safe drop. It is
// never truncated, hashed, or silently admitted with a different identity.
package a2adelegation
