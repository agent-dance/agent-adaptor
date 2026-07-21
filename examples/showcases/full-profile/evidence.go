package main

import (
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func runResultEvidence(result agentadaptor.RunResult) map[string]any {
	return map[string]any{
		"driver_type": result.DriverType,
		"exit_code":   result.ExitCode,
		"timed_out":   result.TimedOut,
		"failure":     result.Failure,
		"run_id":      result.RunID,
		"session":     result.Session,
		"summary":     result.Summary,
		"output":      strings.TrimSpace(result.Output),
		"raw_stdout":  streamSnippet(result.RawStreams, true),
		"raw_stderr":  streamSnippet(result.RawStreams, false),
	}
}

func summarizeProfile(snapshot agentadaptor.ProfileSnapshot) []map[string]any {
	out := make([]map[string]any, 0, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		out = append(out, map[string]any{
			"kind":            resource.Kind,
			"managed":         resource.Managed,
			"external":        resource.External,
			"support":         resource.Support,
			"materialization": resource.Materialization,
			"warnings":        resource.Warnings,
			"error":           resource.Error,
		})
	}
	return out
}

func collectEvidence(layout providerLayout, profile, workspace string) map[string]any {
	files := map[string]string{
		"manifest": filepath.Join(profile, ".agent-adaptor-profile-manifest.json"),
		"skill":    filepath.Join(profile, "skills", "profile-observer"),
		"subagent": layout.SubagentPath,
	}
	for key, path := range layout.MCPFiles {
		files["mcp_"+key] = path
	}
	for key, path := range layout.HookFiles {
		files["hooks_"+key] = path
	}
	for key, path := range layout.InstructionFiles {
		files["instructions_"+key] = path
	}
	if layout.Agent == exampleutil.AgentCursor {
		files["instructions_workspace_rule"] = filepath.Join(workspace, ".cursor", "rules", "full-profile-demo.mdc")
	}

	out := map[string]any{}
	for key, path := range files {
		out[key] = fileEvidence(path)
	}
	auth := map[string]any{}
	for _, name := range layout.AuthFiles {
		auth[name] = redactedAuthEvidence(filepath.Join(profile, name))
	}
	out["auth"] = auth
	return out
}

func fileEvidence(path string) map[string]any {
	info, err := os.Lstat(path)
	if err != nil {
		return map[string]any{"path": path, "exists": false}
	}
	evidence := map[string]any{
		"path":   path,
		"exists": true,
		"mode":   info.Mode().String(),
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err == nil {
			evidence["symlink_target"] = target
		}
		return evidence
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err == nil {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			evidence["entries"] = names
		}
		return evidence
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		text := string(raw)
		if len(text) > 3000 {
			text = text[:3000] + "\n...<truncated>"
		}
		evidence["content"] = text
	}
	return evidence
}

func redactedAuthEvidence(path string) map[string]any {
	info, err := os.Lstat(path)
	if err != nil {
		return map[string]any{"path": path, "exists": false}
	}
	out := map[string]any{
		"path":             path,
		"exists":           true,
		"mode":             info.Mode().String(),
		"size_bytes":       info.Size(),
		"content_redacted": true,
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(path); err == nil {
			out["symlink_target"] = target
		}
	}
	return out
}

func streamSnippet(streams *agentadaptor.RawStreams, stdout bool) string {
	if streams == nil {
		return ""
	}
	value := streams.Stderr
	if stdout {
		value = streams.Stdout
	}
	value = strings.TrimSpace(value)
	if len(value) > 3000 {
		return value[:3000] + "\n...<truncated>"
	}
	return value
}

func limitText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 3000 {
		return value[:3000] + "\n...<truncated>"
	}
	return value
}

func readText(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}
