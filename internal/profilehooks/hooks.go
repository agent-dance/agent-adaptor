package profilehooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

const (
	resourceKind     = string(engine.ProfileResourceHooks)
	shellToolMatcher = "Bash"
	mcpToolMatcher   = "mcp__.*"
	fileReadMatcher  = "Read"
	fileEditMatcher  = "Edit|Write|apply_patch"
)

type providerHook struct {
	Key     string
	Event   string
	Matcher string
	Handler map[string]any
}

func Snapshot(driverType, profileDir string, payload driver.HookPayload, synced bool) engine.ResourceSnapshot {
	out := engine.ResourceSnapshot{
		Kind:            engine.ProfileResourceHooks,
		Fingerprint:     payload.Fingerprint,
		Support:         engine.ProfileResourceSupportPortableCore,
		Materialization: engine.ProfileResourceMaterializationNotMaterialized,
		Warnings:        cloneStrings(payload.Warnings),
	}
	if len(payload.Hooks) == 0 {
		return out
	}
	warnings := collectWarnings(driverType, payload)
	out.Warnings = append(out.Warnings, warnings...)
	if len(warnings) > 0 || hasExtendedHandlers(payload) {
		out.Support = engine.ProfileResourceSupportPortableExtended
	}
	if synced {
		for _, spec := range payload.Hooks {
			out.Managed = append(out.Managed, spec.Key)
		}
		sort.Strings(out.Managed)
		out.Materialization = engine.ProfileResourceMaterializationNativeManaged
	} else {
		out.Warnings = append(out.Warnings, "hook resources are desired but not observed by ProfileSnapshot; call SyncProfile to materialize them")
	}
	return out
}

func Sync(ctx context.Context, driverType, profileDir string, payload driver.HookPayload) (engine.ResourceSnapshot, error) {
	if strings.TrimSpace(profileDir) == "" {
		return engine.ResourceSnapshot{}, fmt.Errorf("profile hooks require profile directory")
	}
	target, err := targetPath(driverType, profileDir)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	lock, err := profilestate.AcquireLock(ctx, profileDir, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	defer lock.Release()

	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	if len(payload.Hooks) == 0 {
		if err := pruneManagedTarget(target, &manifest); err != nil {
			return engine.ResourceSnapshot{}, err
		}
		if err := profilestate.SaveManifest(profileDir, manifest); err != nil {
			return engine.ResourceSnapshot{}, err
		}
		return Snapshot(driverType, profileDir, payload, true), nil
	}
	if err := ensureTargetAvailable(driverType, target, &manifest); err != nil {
		return engine.ResourceSnapshot{}, err
	}
	hooks := make([]providerHook, 0, len(payload.Hooks))
	for _, spec := range payload.Hooks {
		if spec.Disabled {
			continue
		}
		hook, err := renderHook(driverType, spec)
		if err != nil {
			return engine.ResourceSnapshot{}, fmt.Errorf("hook %q: %w", spec.Key, err)
		}
		hooks = append(hooks, hook)
	}
	raw, err := renderFile(driverType, target, hooks)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	if err := profilestate.AtomicWriteFile(target, raw, 0o644); err != nil {
		return engine.ResourceSnapshot{}, err
	}
	for _, spec := range payload.Hooks {
		manifest.Set(profilestate.ManifestEntry{
			Kind:        resourceKind,
			Key:         spec.Key,
			Path:        target,
			Fingerprint: fingerprint(spec),
			Metadata: map[string]string{
				"provider": driverType,
				"event":    string(spec.Event),
			},
		})
	}
	pruneRemovedHooks(payload, &manifest)
	if err := profilestate.SaveManifest(profileDir, manifest); err != nil {
		return engine.ResourceSnapshot{}, err
	}
	snapshot := Snapshot(driverType, profileDir, payload, true)
	if externalHookFile(target, &manifest) {
		snapshot.External = []string{filepath.Base(target)}
	}
	return snapshot, nil
}

func targetPath(driverType, profileDir string) (string, error) {
	switch driverType {
	case "codex", "cursor":
		return filepath.Join(profileDir, "hooks.json"), nil
	case "claude":
		return filepath.Join(profileDir, "settings.json"), nil
	default:
		return "", fmt.Errorf("profile hooks are unsupported by adapter %q", driverType)
	}
}

func renderHook(driverType string, spec driver.HookSpec) (providerHook, error) {
	event, matcher, err := mapEvent(driverType, spec)
	if err != nil {
		return providerHook{}, err
	}
	handler, err := mapHandler(driverType, spec)
	if err != nil {
		return providerHook{}, err
	}
	return providerHook{Key: spec.Key, Event: event, Matcher: matcher, Handler: handler}, nil
}

func renderFile(driverType, target string, hooks []providerHook) ([]byte, error) {
	grouped := map[string][]map[string]any{}
	for _, hook := range hooks {
		switch driverType {
		case "cursor":
			item := cloneAnyMap(hook.Handler)
			if hook.Matcher != "" {
				item["matcher"] = hook.Matcher
			}
			grouped[hook.Event] = append(grouped[hook.Event], item)
		default:
			group := map[string]any{
				"hooks": []map[string]any{hook.Handler},
			}
			if hook.Matcher != "" {
				group["matcher"] = hook.Matcher
			}
			grouped[hook.Event] = append(grouped[hook.Event], group)
		}
	}
	sortGrouped(grouped)
	root := map[string]any{}
	if driverType == "claude" {
		existing, err := readJSONObject(target)
		if err != nil {
			return nil, err
		}
		root = existing
	}
	root["hooks"] = grouped
	if driverType == "cursor" {
		root["version"] = 1
	}
	raw, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func sortGrouped(grouped map[string][]map[string]any) {
	for _, entries := range grouped {
		sort.SliceStable(entries, func(i, j int) bool {
			return fmt.Sprint(entries[i]) < fmt.Sprint(entries[j])
		})
	}
}

func mapEvent(driverType string, spec driver.HookSpec) (string, string, error) {
	matcher := matcherPattern(spec)
	switch driverType {
	case "codex":
		switch spec.Event {
		case driver.HookEventSessionStart:
			return "SessionStart", matcher, nil
		case driver.HookEventPromptSubmit:
			return "UserPromptSubmit", "", nil
		case driver.HookEventPreTool:
			return "PreToolUse", matcher, nil
		case driver.HookEventPostTool:
			return "PostToolUse", matcher, nil
		case driver.HookEventPermissionRequest:
			return "PermissionRequest", matcher, nil
		case driver.HookEventPreShell:
			return "PreToolUse", defaultMatcher(matcher, shellToolMatcher), nil
		case driver.HookEventPostShell:
			return "PostToolUse", defaultMatcher(matcher, shellToolMatcher), nil
		case driver.HookEventPreMCP:
			return "PreToolUse", defaultMatcher(matcher, mcpToolMatcher), nil
		case driver.HookEventPostMCP:
			return "PostToolUse", defaultMatcher(matcher, mcpToolMatcher), nil
		case driver.HookEventPreFileRead:
			return "PreToolUse", defaultMatcher(matcher, fileReadMatcher), nil
		case driver.HookEventPostFileEdit:
			return "PostToolUse", defaultMatcher(matcher, fileEditMatcher), nil
		case driver.HookEventStop:
			return "Stop", "", nil
		default:
			return "", "", fmt.Errorf("event %q is unsupported by Codex hooks", spec.Event)
		}
	case "claude":
		switch spec.Event {
		case driver.HookEventPreShell:
			return "PreToolUse", defaultMatcher(matcher, shellToolMatcher), nil
		case driver.HookEventPostShell:
			return "PostToolUse", defaultMatcher(matcher, shellToolMatcher), nil
		case driver.HookEventPreMCP:
			return "PreToolUse", defaultMatcher(matcher, mcpToolMatcher), nil
		case driver.HookEventPostMCP:
			return "PostToolUse", defaultMatcher(matcher, mcpToolMatcher), nil
		case driver.HookEventPreFileRead:
			return "PreToolUse", defaultMatcher(matcher, fileReadMatcher), nil
		case driver.HookEventPostFileEdit:
			return "PostToolUse", defaultMatcher(matcher, fileEditMatcher), nil
		}
		event, ok := claudeEvents()[spec.Event]
		if !ok {
			return "", "", fmt.Errorf("event %q is unsupported by Claude hooks", spec.Event)
		}
		return event, matcher, nil
	case "cursor":
		event, ok := cursorEvents()[spec.Event]
		if !ok {
			return "", "", fmt.Errorf("event %q is unsupported by Cursor hooks", spec.Event)
		}
		return event, matcher, nil
	default:
		return "", "", fmt.Errorf("profile hooks are unsupported by adapter %q", driverType)
	}
}

func claudeEvents() map[driver.HookEvent]string {
	return map[driver.HookEvent]string{
		driver.HookEventSessionStart:      "SessionStart",
		driver.HookEventSessionEnd:        "SessionEnd",
		driver.HookEventPromptSubmit:      "UserPromptSubmit",
		driver.HookEventPromptExpand:      "UserPromptExpansion",
		driver.HookEventPreTool:           "PreToolUse",
		driver.HookEventPostTool:          "PostToolUse",
		driver.HookEventToolFailure:       "PostToolUseFailure",
		driver.HookEventPermissionRequest: "PermissionRequest",
		driver.HookEventPreShell:          "PreToolUse",
		driver.HookEventPostShell:         "PostToolUse",
		driver.HookEventPreMCP:            "PreToolUse",
		driver.HookEventPostMCP:           "PostToolUse",
		driver.HookEventPreFileRead:       "PreToolUse",
		driver.HookEventPostFileEdit:      "PostToolUse",
		driver.HookEventSubagentStart:     "SubagentStart",
		driver.HookEventSubagentStop:      "SubagentStop",
		driver.HookEventPreCompact:        "PreCompact",
		driver.HookEventPostCompact:       "PostCompact",
		driver.HookEventStop:              "Stop",
		driver.HookEventStopFailure:       "StopFailure",
	}
}

func cursorEvents() map[driver.HookEvent]string {
	return map[driver.HookEvent]string{
		driver.HookEventSessionStart:  "sessionStart",
		driver.HookEventSessionEnd:    "sessionEnd",
		driver.HookEventPromptSubmit:  "beforeSubmitPrompt",
		driver.HookEventPreTool:       "preToolUse",
		driver.HookEventPostTool:      "postToolUse",
		driver.HookEventToolFailure:   "postToolUseFailure",
		driver.HookEventPreShell:      "beforeShellExecution",
		driver.HookEventPostShell:     "afterShellExecution",
		driver.HookEventPreMCP:        "beforeMCPExecution",
		driver.HookEventPostMCP:       "afterMCPExecution",
		driver.HookEventPreFileRead:   "beforeReadFile",
		driver.HookEventPostFileEdit:  "afterFileEdit",
		driver.HookEventSubagentStart: "subagentStart",
		driver.HookEventSubagentStop:  "subagentStop",
		driver.HookEventPreCompact:    "preCompact",
		driver.HookEventStop:          "stop",
	}
}

func mapHandler(driverType string, spec driver.HookSpec) (map[string]any, error) {
	handler := spec.Handler
	switch handler.Type {
	case driver.HookHandlerCommand:
		out := map[string]any{
			"type":    "command",
			"command": commandString(handler),
		}
		addCommonHandlerFields(driverType, spec, out)
		return out, nil
	case driver.HookHandlerPrompt:
		if driverType != "claude" && driverType != "cursor" {
			return nil, fmt.Errorf("prompt hooks are unsupported by %s", driverType)
		}
		out := map[string]any{"type": "prompt", "prompt": handler.Prompt}
		addCommonHandlerFields(driverType, spec, out)
		return out, nil
	case driver.HookHandlerHTTP:
		if driverType != "claude" {
			return nil, fmt.Errorf("http hooks are unsupported by %s", driverType)
		}
		out := map[string]any{"type": "http", "url": handler.URL}
		addCommonHandlerFields(driverType, spec, out)
		return out, nil
	case driver.HookHandlerMCPTool:
		if driverType != "claude" {
			return nil, fmt.Errorf("mcp_tool hooks are unsupported by %s", driverType)
		}
		out := map[string]any{"type": "mcp_tool", "server": handler.Server, "tool": handler.Tool}
		if len(handler.Input) > 0 {
			out["input"] = cloneAnyMap(handler.Input)
		}
		addCommonHandlerFields(driverType, spec, out)
		return out, nil
	case driver.HookHandlerAgent:
		if driverType != "claude" {
			return nil, fmt.Errorf("agent hooks are unsupported by %s", driverType)
		}
		out := map[string]any{"type": "agent", "agent": handler.Agent}
		addCommonHandlerFields(driverType, spec, out)
		return out, nil
	default:
		return nil, fmt.Errorf("handler type %q is unsupported", handler.Type)
	}
}

func addCommonHandlerFields(driverType string, spec driver.HookSpec, out map[string]any) {
	if spec.Timeout > 0 {
		out["timeout"] = int(spec.Timeout.Seconds())
	}
	if driverType == "codex" && strings.TrimSpace(spec.StatusMessage) != "" {
		out["statusMessage"] = strings.TrimSpace(spec.StatusMessage)
	}
	if driverType == "cursor" && spec.FailPolicy == driver.HookFailPolicyClosed {
		out["failClosed"] = true
	}
}

func matcherPattern(spec driver.HookSpec) string {
	if strings.TrimSpace(spec.Matcher) != "" {
		return strings.TrimSpace(spec.Matcher)
	}
	pattern := strings.TrimSpace(spec.MatcherSpec.Pattern)
	if pattern == "" {
		return ""
	}
	switch spec.MatcherSpec.Syntax {
	case driver.HookMatcherSyntaxExact:
		return "^" + regexp.QuoteMeta(pattern) + "$"
	case driver.HookMatcherSyntaxPrefix:
		return "^" + regexp.QuoteMeta(pattern)
	case driver.HookMatcherSyntaxContains:
		return regexp.QuoteMeta(pattern)
	default:
		return pattern
	}
}

func defaultMatcher(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func commandString(handler driver.HookHandler) string {
	parts := []string{shellQuote(handler.Command)}
	for _, arg := range handler.Args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%_+=:,./-", r))
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func collectWarnings(driverType string, payload driver.HookPayload) []string {
	warnings := make([]string, 0)
	for _, spec := range payload.Hooks {
		if spec.Disabled {
			warnings = append(warnings, fmt.Sprintf("hook %q is disabled and kept out of provider hook config", spec.Key))
			continue
		}
		if len(spec.Native) > 0 {
			warnings = append(warnings, fmt.Sprintf("hook %q: native hook fields are not materialized by the generic adapter layout", spec.Key))
		}
		if len(spec.Handler.Env) > 0 || len(spec.Env) > 0 {
			warnings = append(warnings, fmt.Sprintf("hook %q: hook env values are not written into provider config; wrap the command if env injection is required", spec.Key))
		}
		if spec.FailPolicy != "" && !(driverType == "cursor" && spec.FailPolicy == driver.HookFailPolicyClosed) {
			warnings = append(warnings, fmt.Sprintf("hook %q: fail policy %q is not mapped for %s", spec.Key, spec.FailPolicy, driverType))
		}
		if strings.TrimSpace(spec.StatusMessage) != "" && driverType != "codex" {
			warnings = append(warnings, fmt.Sprintf("hook %q: status message is only mapped for Codex hooks", spec.Key))
		}
		if _, _, err := mapEvent(driverType, spec); err != nil {
			warnings = append(warnings, fmt.Sprintf("hook %q: %s", spec.Key, err.Error()))
		}
		if _, err := mapHandler(driverType, spec); err != nil {
			warnings = append(warnings, fmt.Sprintf("hook %q: %s", spec.Key, err.Error()))
		}
	}
	sort.Strings(warnings)
	return warnings
}

func hasExtendedHandlers(payload driver.HookPayload) bool {
	for _, spec := range payload.Hooks {
		if spec.Handler.Type != "" && spec.Handler.Type != driver.HookHandlerCommand {
			return true
		}
	}
	return false
}

func ensureTargetAvailable(driverType, target string, manifest *profilestate.Manifest) error {
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	for _, entry := range manifest.KindEntries(resourceKind) {
		if filepath.Clean(entry.Path) == filepath.Clean(target) {
			return nil
		}
	}
	if driverType == "claude" {
		existing, err := readJSONObject(target)
		if err != nil {
			return err
		}
		if hooksEmpty(existing["hooks"]) {
			return nil
		}
	}
	return fmt.Errorf("hook config target %s is occupied by an external entry", target)
}

func externalHookFile(target string, manifest *profilestate.Manifest) bool {
	if _, err := os.Lstat(target); err != nil {
		return false
	}
	for _, entry := range manifest.KindEntries(resourceKind) {
		if filepath.Clean(entry.Path) == filepath.Clean(target) {
			return false
		}
	}
	return true
}

func pruneManagedTarget(target string, manifest *profilestate.Manifest) error {
	managed := false
	for _, entry := range manifest.KindEntries(resourceKind) {
		if filepath.Clean(entry.Path) == filepath.Clean(target) {
			managed = true
		}
		manifest.Remove(resourceKind, entry.Key)
	}
	if managed {
		if filepath.Base(target) == "settings.json" {
			existing, err := readJSONObject(target)
			if err != nil {
				return err
			}
			delete(existing, "hooks")
			raw, err := json.MarshalIndent(existing, "", "  ")
			if err != nil {
				return err
			}
			return profilestate.AtomicWriteFile(target, append(raw, '\n'), 0o644)
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func readJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("read hook config %s: %w", path, err)
	}
	return out, nil
}

func hooksEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case map[string]any:
		return len(typed) == 0
	case map[string][]map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func pruneRemovedHooks(payload driver.HookPayload, manifest *profilestate.Manifest) {
	desired := map[string]struct{}{}
	for _, spec := range payload.Hooks {
		desired[spec.Key] = struct{}{}
	}
	for _, entry := range manifest.KindEntries(resourceKind) {
		if _, ok := desired[entry.Key]; !ok {
			manifest.Remove(resourceKind, entry.Key)
		}
	}
}

func fingerprint(spec driver.HookSpec) string {
	raw, err := json.Marshal(spec)
	if err != nil {
		raw = []byte(spec.Key + string(spec.Event) + spec.Command + strconv.FormatBool(spec.Disabled))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}
