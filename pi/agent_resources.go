package pi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

// Plans capture resources before the parent receives its prompt. Children use
// these exact bytes/connections, without rediscovering a changed durable scope.
type agentResources struct {
	MCP    driver.MCPPayload
	Skills []agentPreload
}

type agentPreload struct {
	Key     string `json:"key"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

func resolveAgentResources(ctx context.Context, req driver.Request, root string) (map[string]agentResources, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := map[string]agentResources{}
	var catalog []driver.SnapshotEntry
	for _, agent := range req.ProfilePayload.Agents.Agents {
		if len(agent.Skills) > 0 {
			snapshot, err := skillruntime.ListProfileSkills(DriverType, root, req.Skills, req.Skills.Keys(), nil)
			if err != nil {
				return nil, err
			}
			catalog = snapshot.Entries
			break
		}
	}
	for _, agent := range req.ProfilePayload.Agents.Agents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		plan := agentResources{MCP: req.MCP}
		if len(agent.MCPServers) > 0 {
			var servers []driver.MCPServerSpec
			for _, key := range agent.MCPServers {
				found := false
				for _, server := range req.MCP.Servers {
					if server.Key == key {
						servers = append(servers, server)
						found = true
						break
					}
				}
				if !found {
					return nil, fmt.Errorf("pi: subagent %q references unavailable MCP server %q", agent.RuntimeName, key)
				}
			}
			var err error
			plan.MCP, err = engine.ResolveMCPPayload(&engine.MCPConfig{Servers: servers}, nil, configuredDriver{}.Descriptor().MCP)
			if err != nil {
				return nil, err
			}
		}
		seen := map[string]bool{}
		for _, name := range agent.Skills {
			entry, err := findAgentSkill(catalog, name)
			if err != nil {
				return nil, fmt.Errorf("pi: subagent %q: %w", agent.RuntimeName, err)
			}
			path := filepath.Join(root, "skills", entry.RuntimeName, "SKILL.md")
			// Explicit materialized selections override native installations.
			if entry.Selected {
				path = filepath.Join(entry.SourcePath, "SKILL.md")
			}
			path, err = filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			if seen[path] {
				continue
			}
			seen[path] = true
			content, err := readAgentSkill(ctx, path)
			if err != nil {
				return nil, fmt.Errorf("pi: subagent %q skill %q: %w", agent.RuntimeName, name, err)
			}
			plan.Skills = append(plan.Skills, agentPreload{Key: entry.Key, Path: path, Content: content})
		}
		out[agent.RuntimeName] = plan
	}
	return out, nil
}

func findAgentSkill(catalog []driver.SnapshotEntry, name string) (driver.SnapshotEntry, error) {
	// Host keys take precedence; runtime names are accepted only if unambiguous.
	for _, byKey := range []bool{true, false} {
		var matches []driver.SnapshotEntry
		for _, entry := range catalog {
			if (byKey && entry.Key == name) || (!byKey && entry.RuntimeName == name) {
				matches = append(matches, entry)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return driver.SnapshotEntry{}, fmt.Errorf("ambiguous skill %q", name)
		}
	}
	return driver.SnapshotEntry{}, fmt.Errorf("unavailable skill %q", name)
}

const maxAgentSkillBytes = 4 << 20

func readAgentSkill(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxAgentSkillBytes {
		return "", errors.New("preloaded SKILL.md must be a regular file no larger than 4 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxAgentSkillBytes+1))
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(raw) > maxAgentSkillBytes || !utf8.Valid(raw) || strings.TrimSpace(string(raw)) == "" {
		return "", errors.New("preloaded SKILL.md must be nonempty UTF-8 text no larger than 4 MiB")
	}
	return string(raw), nil
}

// The native before_agent_start hook supplies the current chained prompt.
// Include the source path so relative references in the skill remain useful.
const agentPreloadExtension = `
function installSkillPreloads(pi) {
  if (!skillPreloads.length) return;
  pi.on("before_agent_start", event => ({
    systemPrompt: event.systemPrompt + "\n\nPreloaded skills for this agent:\n" + skillPreloads.map(skill =>
      "\nSkill " + JSON.stringify(skill.key) + " (source: " + JSON.stringify(skill.path) + "):\n" + skill.content
    ).join("\n")
  }));
}
`
