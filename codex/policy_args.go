package codex

import (
	"fmt"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

// codexPolicyArgs translates the provider-neutral run policy into Codex's
// documented root flags. Each dimension is independent: unrestricted sandbox
// access must not implicitly disable approvals.
func codexPolicyArgs(policy driver.RunPolicy) []string {
	args := make([]string, 0, 8)
	switch policy.WebSearch {
	case driver.FeatureAllow:
		args = append(args, "--search")
	case driver.FeatureDeny:
		args = append(args, "-c", `web_search="disabled"`)
	}
	switch policy.Isolation {
	case driver.IsolationReadOnly:
		args = append(args, "--sandbox", "read-only")
	case driver.IsolationWorkspaceWrite:
		args = append(args, "--sandbox", "workspace-write")
	case driver.IsolationUnrestricted:
		args = append(args, "--sandbox", "danger-full-access")
	}
	effectiveApproval := driver.EffectiveHumanDecisionPolicy(policy.HumanDecision).Permission
	switch effectiveApproval {
	case driver.HumanDecisionAutoApprove:
		args = append(args, "--ask-for-approval", "never")
	case driver.HumanDecisionAsk, driver.HumanDecisionAutoReject:
		args = append(args, "--ask-for-approval", "on-request")
	}
	return args
}

// codexAppServerExtraArgs projects constructor ExtraArgs onto the official
// app-server command surface. Exec-only flags are rejected before launch so a
// fork cannot silently run with different construction configuration.
func codexAppServerExtraArgs(extra []string, policy driver.RunPolicy) ([]string, error) {
	filtered := filterCodexPolicyExtraArgs(extra, policy)
	out := append([]string(nil), codexAppServerPolicyArgs(policy)...)
	for i := 0; i < len(filtered); i++ {
		arg := filtered[i]
		base, value, inline := splitCodexArg(arg)
		switch base {
		case "-c", "--config", "--enable", "--disable":
			if inline {
				if strings.TrimSpace(value) == "" {
					return nil, invalidCodexAppServerArg(arg, "value is required")
				}
				out = append(out, arg)
				continue
			}
			if i+1 >= len(filtered) || !isCodexDetachedArgValue(filtered[i+1]) {
				return nil, invalidCodexAppServerArg(arg, "value is required")
			}
			out = append(out, arg, filtered[i+1])
			i++
		case "--strict-config", "--analytics-default-enabled":
			if inline {
				return nil, invalidCodexAppServerArg(arg, "flag does not accept an inline value")
			}
			out = append(out, arg)
		default:
			return nil, invalidCodexAppServerArg(arg, "exec-only or unsupported app-server argument")
		}
	}
	return out, nil
}

func invalidCodexAppServerArg(arg, reason string) error {
	return &driver.InvalidDriverConfigError{
		Driver: DriverType,
		Cause:  fmt.Errorf("ExtraArgs argument %q cannot be used with codex app-server: %s", arg, reason),
	}
}

// codexAppServerPolicyArgs applies dimensions which are not represented in
// thread/start or turn/start. The app-server process accepts the same Codex
// config overrides as the root command.
func codexAppServerPolicyArgs(policy driver.RunPolicy) []string {
	switch policy.WebSearch {
	case driver.FeatureAllow:
		return []string{"-c", `web_search="live"`}
	case driver.FeatureDeny:
		return []string{"-c", `web_search="disabled"`}
	default:
		return nil
	}
}

// filterCodexPolicyExtraArgs prevents constructor ExtraArgs from overriding
// SDK-owned policy dimensions. Filtering is unconditional: an inherited call
// value still means the SDK/provider default, not a hidden constructor-level
// bypass. Unrelated config overrides and flags are preserved.
func filterCodexPolicyExtraArgs(extra []string, _ driver.RunPolicy) []string {
	out := make([]string, 0, len(extra))
	for i := 0; i < len(extra); i++ {
		arg := extra[i]
		base, inlineValue, hasInlineValue := splitCodexArg(arg)
		switch base {
		case "--search":
			continue
		case "--sandbox", "-s":
			if !hasInlineValue && i+1 < len(extra) && isCodexDetachedArgValue(extra[i+1]) {
				i++
			}
			continue
		case "--ask-for-approval", "-a":
			if !hasInlineValue && i+1 < len(extra) && isCodexDetachedArgValue(extra[i+1]) {
				i++
			}
			continue
		case "--dangerously-bypass-approvals-and-sandbox", "--full-auto":
			continue
		case "--enable", "--disable":
			value := inlineValue
			if !hasInlineValue && i+1 < len(extra) && isCodexDetachedArgValue(extra[i+1]) {
				value = extra[i+1]
			}
			if managedCodexFeature(value) {
				if !hasInlineValue && i+1 < len(extra) && isCodexDetachedArgValue(extra[i+1]) {
					i++
				}
				continue
			}
		case "-c", "--config":
			value := inlineValue
			if !hasInlineValue && i+1 < len(extra) && isCodexDetachedArgValue(extra[i+1]) {
				value = extra[i+1]
			}
			if managedCodexConfig(value, true, true, true) {
				if !hasInlineValue && i+1 < len(extra) && isCodexDetachedArgValue(extra[i+1]) {
					i++
				}
				continue
			}
		}
		out = append(out, arg)
	}
	return out
}

func managedCodexFeature(value string) bool {
	switch strings.Trim(strings.TrimSpace(value), `"'`) {
	case "web_search_request", "web_search":
		return true
	default:
		return false
	}
}

func isCodexDetachedArgValue(arg string) bool {
	value := strings.TrimSpace(arg)
	return value != "" && !strings.HasPrefix(value, "-")
}

func splitCodexArg(arg string) (base, value string, hasValue bool) {
	// Clap accepts short options with an attached value (for example -anever
	// and -cweb_search=live). Recognize those forms so constructor ExtraArgs
	// cannot bypass SDK-owned policy filtering through alternate argv syntax.
	if len(arg) > 2 && arg[0] == '-' && arg[1] != '-' {
		switch arg[:2] {
		case "-s", "-a", "-c":
			value := strings.TrimPrefix(arg[2:], "=")
			return arg[:2], value, true
		}
	}
	if eq := strings.IndexByte(arg, '='); eq >= 0 {
		return arg[:eq], arg[eq+1:], true
	}
	return arg, "", false
}

func managedCodexConfig(value string, web, isolation, approval bool) bool {
	key := strings.TrimSpace(value)
	if eq := strings.IndexByte(key, '='); eq >= 0 {
		key = strings.TrimSpace(key[:eq])
	}
	switch key {
	case "web_search", "features.web_search_request", "tools.web_search":
		return web
	case "sandbox_mode":
		return isolation
	case "approval_policy":
		return approval
	default:
		return false
	}
}
