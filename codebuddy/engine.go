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
// for an interactive decision (Ask) or a faithful local rejection
// (AutoReject) on any of the three decision kinds — headless `-p` mode has no
// way to surface or reject a specific tool/plan/question decision.
//
// AutoApprove alone keeps the run on the headless engine, where the
// permission mode flag grants the equivalent blanket approval.
func wantsControlTransport(p driver.HumanDecisionPolicy) bool {
	switch p.Permission {
	case driver.HumanDecisionAsk, driver.HumanDecisionAutoReject:
		return true
	}
	switch p.PlanReview {
	case driver.HumanDecisionAsk, driver.HumanDecisionAutoReject:
		return true
	}
	switch p.Question {
	case driver.QuestionAsk, driver.QuestionAutoReject:
		return true
	}
	return false
}

// headlessPermissionMode derives the CodeBuddy `--permission-mode` value for a
// headless run. An explicit config override always wins. Otherwise the mode is
// derived from the effective permission policy: AutoApprove grants blanket
// approval (bypassPermissions) so Bash/Write/Edit run without a prompt, which
// matches the AutoApprove intent in a non-interactive engine. Anything else
// falls back to the CLI default (Always Ask), which in headless `-p` mode
// results in the CLI declining tools that would otherwise prompt.
func headlessPermissionMode(cfg Config, policy driver.RunPolicy) PermissionMode {
	if cfg.PermissionMode != PermissionUnset {
		return cfg.PermissionMode
	}
	effective := driver.EffectiveHumanDecisionPolicy(policy.HumanDecision)
	if effective.Permission == driver.HumanDecisionAutoApprove {
		return PermissionBypass
	}
	return PermissionDefault
}
