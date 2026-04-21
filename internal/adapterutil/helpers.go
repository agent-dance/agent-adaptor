package adapterutil

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func CommandEnvironmentChecks(command string) []agentadaptor.EnvironmentCheck {
	command = strings.TrimSpace(command)
	if command == "" {
		return []agentadaptor.EnvironmentCheck{{
			Code:    "command_missing",
			Level:   "error",
			Message: "no command configured",
			Hint:    "Set CommonConfig.Command or rely on the adapter default CLI name.",
		}}
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return []agentadaptor.EnvironmentCheck{{
			Code:    "command_missing",
			Level:   "error",
			Message: err.Error(),
			Detail:  command,
			Hint:    "Install the CLI or update CommonConfig.Command to a resolvable executable.",
		}}
	}
	return []agentadaptor.EnvironmentCheck{{
		Code:    "command_found",
		Level:   "info",
		Message: "command resolved",
		Detail:  resolved,
	}}
}

func CWDEnvironmentChecks(cwd string) []agentadaptor.EnvironmentCheck {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return []agentadaptor.EnvironmentCheck{{
			Code:    "cwd_default",
			Level:   "info",
			Message: "working directory will be resolved from the workspace manager",
		}}
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return []agentadaptor.EnvironmentCheck{{
			Code:    "cwd_invalid",
			Level:   "error",
			Message: err.Error(),
			Detail:  cwd,
			Hint:    "Create the directory or update CommonConfig.CWD.",
		}}
	}
	if !info.IsDir() {
		return []agentadaptor.EnvironmentCheck{{
			Code:    "cwd_not_directory",
			Level:   "error",
			Message: "configured working directory is not a directory",
			Detail:  cwd,
			Hint:    "Point CommonConfig.CWD at a directory path.",
		}}
	}
	abs, _ := filepath.Abs(cwd)
	return []agentadaptor.EnvironmentCheck{{
		Code:    "cwd_valid",
		Level:   "info",
		Message: "configured working directory exists",
		Detail:  abs,
	}}
}

func SummarizeEnvironment(driverType string, checks []agentadaptor.EnvironmentCheck) agentadaptor.EnvironmentReport {
	report := agentadaptor.EnvironmentReport{
		DriverType: driverType,
		Status:     agentadaptor.EnvironmentPass,
		Healthy:    true,
		Checks:     append([]agentadaptor.EnvironmentCheck(nil), checks...),
	}
	for _, check := range checks {
		switch check.Level {
		case "error":
			report.Status = agentadaptor.EnvironmentFail
			report.Healthy = false
		case "warn":
			if report.Status != agentadaptor.EnvironmentFail {
				report.Status = agentadaptor.EnvironmentWarn
			}
		}
	}
	if len(checks) == 0 {
		report.Summary = "no environment checks reported"
		return report
	}
	switch report.Status {
	case agentadaptor.EnvironmentFail:
		report.Summary = "environment checks failed"
	case agentadaptor.EnvironmentWarn:
		report.Summary = "environment checks completed with warnings"
	default:
		report.Summary = "environment checks passed"
	}
	return report
}

func ResolvedEnvValue(bindings []agentadaptor.EnvBinding, name string) (string, string) {
	if value := strings.TrimSpace(skillruntime.ResolveBinding(bindings, name)); value != "" {
		return value, "binding_env"
	}
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, "process_env"
	}
	return "", ""
}

func ResolvedTruthyEnv(bindings []agentadaptor.EnvBinding, name string) (bool, string) {
	value, source := ResolvedEnvValue(bindings, name)
	if value == "" {
		return false, ""
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, source
	default:
		return false, ""
	}
}

func RuntimeEnvBindings(bindings []agentadaptor.EnvBinding, payload agentadaptor.RuntimePayload) ([]agentadaptor.EnvBinding, error) {
	if len(payload.Ensured) == 0 {
		return append([]agentadaptor.EnvBinding(nil), bindings...), nil
	}
	raw, err := json.Marshal(payload.Ensured)
	if err != nil {
		return nil, err
	}
	out := append([]agentadaptor.EnvBinding(nil), bindings...)
	out = append(out, agentadaptor.EnvBinding{Name: "PAPERCLIP_RUNTIME_SERVICES_JSON", Value: string(raw)})
	return out, nil
}

func RuntimePromptPrefix(payload agentadaptor.RuntimePayload) string {
	if len(payload.Ensured) == 0 {
		return ""
	}
	lines := make([]string, 0, len(payload.Ensured)+1)
	lines = append(lines, "Runtime services available:")
	for _, service := range payload.Ensured {
		label := strings.TrimSpace(service.Name)
		if label == "" {
			label = strings.TrimSpace(service.ID)
		}
		if label == "" {
			label = "service"
		}
		annotations := make([]string, 0, 2)
		if service.Lifecycle != "" {
			annotations = append(annotations, string(service.Lifecycle))
		}
		if service.Health != "" && service.Health != agentadaptor.RuntimeHealthUnknown {
			annotations = append(annotations, string(service.Health))
		}
		if len(annotations) > 0 {
			label = fmt.Sprintf("%s (%s)", label, strings.Join(annotations, ", "))
		}
		if strings.TrimSpace(service.URL) != "" {
			lines = append(lines, fmt.Sprintf("- %s: %s", label, service.URL))
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s", label))
	}
	return strings.Join(lines, "\n")
}

func RuntimeReportsFromRefs(refs []agentadaptor.RuntimeServiceRef, owner agentadaptor.AgentIdentity) []agentadaptor.RuntimeServiceReport {
	if len(refs) == 0 {
		return nil
	}
	out := make([]agentadaptor.RuntimeServiceReport, 0, len(refs))
	for _, ref := range refs {
		status := ref.Status
		if status == "" {
			status = agentadaptor.RuntimeServiceRunning
		}
		lifecycle := ref.Lifecycle
		if lifecycle == "" {
			lifecycle = agentadaptor.RuntimeLifecycleShared
		}
		ownerID := ref.OwnerAgentID
		if ownerID == "" {
			ownerID = owner.ID
		}
		health := ref.Health
		if health == "" {
			health = agentadaptor.RuntimeHealthUnknown
		}
		out = append(out, agentadaptor.RuntimeServiceReport{
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

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
