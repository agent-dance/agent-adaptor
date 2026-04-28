package profileconfig

import (
	"fmt"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type capabilitySpec struct {
	Name        string
	Label       string
	Description string
	Values      string
	Support     agentadaptor.ProfileResourceSupport
}

func CapabilityFields(driverType string) []agentadaptor.ConfigField {
	specs := capabilitySpecs(driverType)
	out := make([]agentadaptor.ConfigField, 0, len(specs))
	for _, spec := range specs {
		out = append(out, agentadaptor.ConfigField{
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
				"materialization":  string(agentadaptor.ProfileResourceMaterializationNativeManaged),
			},
		})
	}
	return out
}

func capabilitySpecs(driverType string) []capabilitySpec {
	switch driverType {
	case "codex":
		return []capabilitySpec{
			{Name: "model", Label: "Profile Model", Description: "Persist Codex model in config.toml.", Values: `"model": string`, Support: agentadaptor.ProfileResourceSupportPortableCore},
			{Name: "reasoning_effort", Label: "Profile Reasoning Effort", Description: "Persist Codex model reasoning effort in config.toml.", Values: `"effort": "low|medium|high|xhigh"`, Support: agentadaptor.ProfileResourceSupportPortableExtended},
			{Name: "sandbox", Label: "Profile Sandbox", Description: "Persist Codex sandbox mode in config.toml.", Values: `"mode": "read-only|workspace-write|danger-full-access"`, Support: agentadaptor.ProfileResourceSupportPortableExtended},
			{Name: "approval", Label: "Profile Approval Policy", Description: "Persist Codex approval policy in config.toml.", Values: `"mode": string`, Support: agentadaptor.ProfileResourceSupportPortableExtended},
		}
	case "claude":
		return []capabilitySpec{
			{Name: "model", Label: "Profile Model", Description: "Persist Claude model in settings.json.", Values: `"model": string`, Support: agentadaptor.ProfileResourceSupportPortableCore},
			{Name: "effort", Label: "Profile Effort", Description: "Persist Claude effort in settings.json.", Values: `"effort": "low|medium|high|xhigh|max"`, Support: agentadaptor.ProfileResourceSupportPortableExtended},
			{Name: "permission", Label: "Profile Permission Mode", Description: "Persist Claude permission mode in settings.json.", Values: `"mode": string`, Support: agentadaptor.ProfileResourceSupportPortableExtended},
			{Name: "env", Label: "Profile Environment", Description: "Persist Claude environment entries under settings.json env.", Values: `"NAME": string`, Support: agentadaptor.ProfileResourceSupportPortableExtended},
		}
	case "cursor":
		return []capabilitySpec{
			{Name: "sandbox", Label: "Profile Sandbox", Description: "Persist Cursor CLI sandbox config in cli-config.json.", Values: `"mode": "enabled|disabled"`, Support: agentadaptor.ProfileResourceSupportPortableExtended},
			{Name: "approval", Label: "Profile Approval Mode", Description: "Persist Cursor CLI approval mode in cli-config.json.", Values: `"mode": "allowlist|unrestricted"`, Support: agentadaptor.ProfileResourceSupportPortableExtended},
			{Name: "permissions", Label: "Profile Permissions", Description: "Persist Cursor CLI allow/deny permission patterns.", Values: `"allow": []string, "deny": []string`, Support: agentadaptor.ProfileResourceSupportPortableExtended},
			{Name: "display", Label: "Profile Display", Description: "Persist Cursor CLI display preferences.", Values: `"showThinkingBlocks": bool`, Support: agentadaptor.ProfileResourceSupportPortableExtended},
		}
	default:
		return nil
	}
}
