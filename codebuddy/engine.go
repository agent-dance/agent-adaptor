package codebuddy

import (
	"github.com/agent-dance/agent-adaptor/driver"
)

// wantsControlTransport reports whether the run policy requires the
// bidirectional CodeBuddy SDK control transport.
//
// Like the Claude driver's interactive gate, this inspects the raw policy
// fields (not the effective defaults) so a zero-value policy stays on the
// headless engine. The control transport engages whenever the host explicitly asks
// for an interactive decision (Ask), a faithful local rejection (AutoReject),
// or plan-only approval (AutoApprove). Headless `-p` mode has no way to
// surface or apply a policy to a specific tool/plan/question decision.
//
// Permission AutoApprove alone, or Permission+PlanReview AutoApprove, keeps
// the run on the headless engine, where bypassPermissions grants the requested
// positive decisions without requiring a bidirectional decision sink.
func wantsControlTransport(p driver.HumanDecisionPolicy) bool {
	switch p.Permission {
	case driver.HumanDecisionAsk, driver.HumanDecisionAutoReject:
		return true
	}
	switch p.PlanReview {
	case driver.HumanDecisionAsk, driver.HumanDecisionAutoReject:
		return true
	case driver.HumanDecisionAutoApprove:
		// Plan-only approval cannot use blanket bypass without broadening an
		// inherited or restrictive permission policy.
		return p.Permission != driver.HumanDecisionAutoApprove
	}
	switch p.Question {
	case driver.QuestionAsk, driver.QuestionAutoReject:
		return true
	}
	return false
}

// headlessPermissionMode derives the CodeBuddy `--permission-mode` value for a
// headless run. An explicit per-call permission policy wins over the
// constructor default. Otherwise the mode is derived from the effective
// permission policy: AutoApprove grants blanket
// approval (bypassPermissions) so Bash/Write/Edit run without a prompt, which
// matches the AutoApprove intent in a non-interactive engine. Anything else
// falls back to the CLI default (Always Ask), which in headless `-p` mode
// results in the CLI declining tools that would otherwise prompt.
func headlessPermissionMode(cfg Config, policy driver.RunPolicy) PermissionMode {
	if policy.HumanDecision.Permission == driver.HumanDecisionAutoApprove {
		return PermissionBypass
	}
	if cfg.PermissionMode != PermissionUnset {
		return cfg.PermissionMode
	}
	effective := driver.EffectiveHumanDecisionPolicy(policy.HumanDecision)
	if effective.Permission == driver.HumanDecisionAutoApprove {
		return PermissionBypass
	}
	return PermissionDefault
}
