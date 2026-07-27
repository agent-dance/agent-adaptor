package profileconfig

import (
	"fmt"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

type capabilitySpec struct {
	Name        string
	Label       string
	Description string
	Values      string
	Support     engine.ProfileResourceSupport
}

func CapabilityFields(driverType string) []driver.ConfigField {
	specs := capabilitySpecs(driverType)
	out := make([]driver.ConfigField, 0, len(specs))
	for _, spec := range specs {
		out = append(out, driver.ConfigField{
			Name:        "profile_config." + spec.Name,
			Label:       spec.Label,
			Type:        "object",
			Description: spec.Description,
			Hint:        fmt.Sprintf("Use ProfileConfigPatch{Capability:%q, Values:{%s}}.", spec.Name, spec.Values),
			Group:       "profile_config",
			Meta: map[string]string{
				"profile_resource": "config",
				"capability":       spec.Name,
				"support":          string(spec.Support),
				"materialization":  string(engine.ProfileResourceMaterializationNativeManaged),
			},
		})
	}
	return out
}

func capabilitySpecs(driverType string) []capabilitySpec {
	switch driverType {
	case "codex":
		return []capabilitySpec{
			{Name: "model", Label: "Profile Model", Description: "Persist Codex model in config.toml.", Values: `"model": string`, Support: engine.ProfileResourceSupportPortableCore},
			{Name: "reasoning_effort", Label: "Profile Reasoning Effort", Description: "Persist Codex model reasoning effort in config.toml.", Values: `"effort": "low|medium|high|xhigh"`, Support: engine.ProfileResourceSupportPortableExtended},
			{Name: "sandbox", Label: "Profile Sandbox", Description: "Persist Codex sandbox mode in config.toml.", Values: `"mode": "read-only|workspace-write|danger-full-access"`, Support: engine.ProfileResourceSupportPortableExtended},
			{Name: "approval", Label: "Profile Approval Policy", Description: "Persist Codex approval policy in config.toml.", Values: `"mode": string`, Support: engine.ProfileResourceSupportPortableExtended},
		}
	case "claude":
		return []capabilitySpec{
			{Name: "model", Label: "Profile Model", Description: "Persist Claude model in settings.json.", Values: `"model": string`, Support: engine.ProfileResourceSupportPortableCore},
			{Name: "effort", Label: "Profile Effort", Description: "Persist Claude effort in settings.json.", Values: `"effort": "low|medium|high|xhigh|max"`, Support: engine.ProfileResourceSupportPortableExtended},
			{Name: "permission", Label: "Profile Permission Mode", Description: "Persist Claude permission mode in settings.json.", Values: `"mode": string`, Support: engine.ProfileResourceSupportPortableExtended},
			{Name: "env", Label: "Profile Environment", Description: "Persist Claude environment entries under settings.json env.", Values: `"NAME": string`, Support: engine.ProfileResourceSupportPortableExtended},
		}
	case "cursor":
		return []capabilitySpec{
			{Name: "sandbox", Label: "Profile Sandbox", Description: "Persist Cursor CLI sandbox config in cli-config.json.", Values: `"mode": "enabled|disabled"`, Support: engine.ProfileResourceSupportPortableExtended},
			{Name: "approval", Label: "Profile Approval Mode", Description: "Persist Cursor CLI approval mode in cli-config.json.", Values: `"mode": "allowlist|unrestricted"`, Support: engine.ProfileResourceSupportPortableExtended},
			{Name: "permissions", Label: "Profile Permissions", Description: "Persist Cursor CLI allow/deny permission patterns.", Values: `"allow": []string, "deny": []string`, Support: engine.ProfileResourceSupportPortableExtended},
			{Name: "display", Label: "Profile Display", Description: "Persist Cursor CLI display preferences.", Values: `"showThinkingBlocks": bool`, Support: engine.ProfileResourceSupportPortableExtended},
		}
	case "codebuddy":
		return []capabilitySpec{
			{Name: "model", Label: "Profile Model", Description: "Persist CodeBuddy model in settings.json.", Values: `"model": string`, Support: engine.ProfileResourceSupportPortableCore},
			{Name: "effort", Label: "Profile Effort", Description: "Persist CodeBuddy effort in settings.json.", Values: `"effort": "low|medium|high|xhigh|max"`, Support: engine.ProfileResourceSupportPortableExtended},
			{Name: "permission", Label: "Profile Permission Mode", Description: "Persist CodeBuddy permission mode in settings.json.", Values: `"mode": string`, Support: engine.ProfileResourceSupportPortableExtended},
			{Name: "env", Label: "Profile Environment", Description: "Persist CodeBuddy environment entries under settings.json env.", Values: `"NAME": string`, Support: engine.ProfileResourceSupportPortableExtended},
		}
	default:
		return nil
	}
}
