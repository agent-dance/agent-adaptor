package doctest

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
)

var (
	markdownLinkPattern   = regexp.MustCompile(`!?\[[^\]]*\]\(([^)]+)\)`)
	headingPattern        = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	examplePathPattern    = regexp.MustCompile(`\./examples/(?:recipes|showcases|tools)/[a-z0-9-]+`)
	catalogPathPattern    = regexp.MustCompile(`\./(?:recipes|showcases|tools)/[a-z0-9-]+`)
	catalogCommandPattern = regexp.MustCompile("`(?:go run |\\./examples/)[^`]+`")
	inlineCodePattern     = regexp.MustCompile("`([^`\\n]+)`")
	publicAPITokenPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:\([^)]*\))?(?:\.[A-Za-z][A-Za-z0-9]*(?:\([^)]*\))?)*$`)
)

func TestPublicMarkdownLinksResolve(t *testing.T) {
	root := repositoryRoot(t)
	for _, name := range publicMarkdownFiles {
		name := name
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, filepath.FromSlash(name))
			body := readFile(t, path)
			for _, match := range markdownLinkPattern.FindAllStringSubmatch(markdownOutsideCode(body), -1) {
				target := strings.TrimSpace(match[1])
				if strings.Contains(target, ` "`) || strings.Contains(target, ` '`) {
					target = strings.Fields(target)[0]
				}
				checkLocalLink(t, root, path, target)
			}
		})
	}
}

func TestRemovedExamplesAreNotAdvertised(t *testing.T) {
	root := repositoryRoot(t)
	removed := []string{
		"mock-runtime-admin",
		"mock-adapter-playground",
		"mock-skills-contract",
	}
	for _, name := range publicMarkdownFiles {
		body := readFile(t, filepath.Join(root, filepath.FromSlash(name)))
		for _, old := range removed {
			if strings.Contains(body, old) {
				t.Errorf("%s still advertises removed example %q", name, old)
			}
		}
	}
}

func TestREADMEParity(t *testing.T) {
	root := repositoryRoot(t)
	english := readFile(t, filepath.Join(root, "README.md"))
	chinese := readFile(t, filepath.Join(root, "README.zh-CN.md"))

	sharedTerms := []string{
		"Build", "New", "WithDefaultAgent", "WithRuntimeServiceManager",
		"WithStreaming", "WithCloneProfile", "WithNativeProfile", "BindTyped",
		"DriverAdapter", "RunPolicy.HumanDecision", "sdk.Run", "sdk.Start",
		"sdk.Agent", "sdk.Admin", "SessionStore",
		"SessionKey", "SessionID", "ThreadID", "RunID", "Output", "RawStreams",
		"Transcript", "Summary", "Result", "StructuredOutput", "Failure", "DecisionRequests",
		"ResolveDecision", "AG-UI", "SSE", "A2A", "HTTP/gRPC", "queue",
		"scheduler", "tenant", "routing",
	}
	for _, term := range sharedTerms {
		if !strings.Contains(english, term) {
			t.Errorf("README.md is missing shared contract term %q", term)
		}
		if !strings.Contains(chinese, term) {
			t.Errorf("README.zh-CN.md is missing shared contract term %q", term)
		}
	}

	if got, want := sortedMatches(examplePathPattern, english), sortedMatches(examplePathPattern, chinese); !reflect.DeepEqual(got, want) {
		t.Errorf("root README example paths differ\nenglish: %v\nchinese: %v", got, want)
	}
	if got, want := fencedBlocks(english, "bash"), fencedBlocks(chinese, "bash"); !reflect.DeepEqual(got, want) {
		t.Errorf("root README shell commands differ\nenglish: %v\nchinese: %v", got, want)
	}
	if got, want := normalizedLocalLinks(english), normalizedLocalLinks(chinese); !reflect.DeepEqual(got, want) {
		t.Errorf("root README local links differ\nenglish: %v\nchinese: %v", got, want)
	}
	if got, want := publicAPITokens(english), publicAPITokens(chinese); !reflect.DeepEqual(got, want) {
		t.Errorf("root README public API tokens differ\nenglish: %v\nchinese: %v", got, want)
	}

	wantEnglishHeadings := []string{
		"Why Not Call Each CLI Directly?", "Quick Start", "Choose An Integration Path",
		"One Execution Lifecycle", "Core Mental Model", "Capabilities And Provider Differences",
		"Session And Identity Dimensions", "Result Contract", "Streaming And Human Decisions",
		"Managed Context And Bridges", "Admin And Custom Adapters", "Examples And Documentation",
		"Non-Goals",
	}
	wantChineseHeadings := []string{
		"为什么不直接调用各家 CLI？", "快速开始", "按产品形态选择集成路径",
		"一套执行生命周期", "核心心智", "能力与 Provider 差异", "Session 与 ID 维度",
		"结果合同", "Streaming 与人工决策", "受控上下文与 Bridge", "Admin 与自定义 Adapter",
		"Examples 与文档", "非目标",
	}
	if got := levelTwoHeadings(english); !reflect.DeepEqual(got, wantEnglishHeadings) {
		t.Errorf("README.md section structure = %v, want %v", got, wantEnglishHeadings)
	}
	if got := levelTwoHeadings(chinese); !reflect.DeepEqual(got, wantChineseHeadings) {
		t.Errorf("README.zh-CN.md section structure = %v, want %v", got, wantChineseHeadings)
	}
	if got, want := sectionBulletCount(english, "## Non-Goals"), sectionBulletCount(chinese, "## 非目标"); got != want {
		t.Errorf("root README non-goal counts differ: english=%d chinese=%d", got, want)
	}
	englishNonGoals := sectionText(english, "## Non-Goals")
	chineseNonGoals := sectionText(chinese, "## 非目标")
	nonGoalSemantics := []struct {
		english []string
		chinese []string
	}{
		{[]string{"HTTP/gRPC"}, []string{"HTTP/gRPC"}},
		{[]string{"queue", "scheduler", "tenant", "authentication", "daemon"}, []string{"queue", "scheduler", "tenant", "authentication", "daemon"}},
		{[]string{"routing", "broker", "planner", "agent selection"}, []string{"routing", "broker", "planner", "Agent 选择"}},
		{[]string{"database", "distributed lock", "stateful"}, []string{"database", "distributed lock", "stateful"}},
		{[]string{"second execution entrypoint", "session", "default-merging"}, []string{"第二套执行入口", "session", "默认值合并"}},
	}
	for i, semantic := range nonGoalSemantics {
		assertContainsAll(t, "README.md non-goal", i, englishNonGoals, semantic.english)
		assertContainsAll(t, "README.zh-CN.md non-goal", i, chineseNonGoals, semantic.chinese)
	}

	capabilityKeys := []string{
		"Execution lifecycle",
		"Session resume",
		"Content streaming",
		"HITL Ask",
		"Structured output",
		"MCP transports",
	}
	for _, key := range capabilityKeys {
		enRow := capabilityStatus(t, english, key)
		zhRow := capabilityStatus(t, chinese, key)
		if !reflect.DeepEqual(enRow, zhRow) {
			t.Errorf("capability %q differs: english=%v chinese=%v", key, enRow, zhRow)
		}
	}
}

func TestQuickStartIsCompiledExample(t *testing.T) {
	root := repositoryRoot(t)
	english := readFile(t, filepath.Join(root, "README.md"))
	chinese := readFile(t, filepath.Join(root, "README.zh-CN.md"))
	example := strings.TrimSpace(readFile(t, filepath.Join(root, "examples/recipes/basic-run/main.go")))

	enSnippet := firstGoBlockAfter(t, english, "## Quick Start")
	zhSnippet := firstGoBlockAfter(t, chinese, "## 快速开始")
	if enSnippet != zhSnippet {
		t.Fatal("English and Chinese Quick Start programs differ")
	}
	if enSnippet != example {
		t.Fatal("root README Quick Start must exactly match examples/recipes/basic-run/main.go")
	}
	if strings.Contains(enSnippet, "/examples/internal/") {
		t.Fatal("Quick Start must not import examples/internal")
	}
}

func TestREADMECapabilitiesMatchDescriptors(t *testing.T) {
	root := repositoryRoot(t)
	body := readFile(t, filepath.Join(root, "README.md"))
	adapters := []agentadaptor.DriverAdapter{codex.NewAdapter(), claude.NewAdapter(), cursor.NewAdapter()}

	expected := map[string][]string{
		"Execution lifecycle": {"yes", "yes", "yes"},
		"Session resume":      descriptorValues(adapters, sessionStatus),
		"Content streaming":   descriptorValues(adapters, streamingStatus),
		"HITL Ask":            descriptorValues(adapters, hitlAskStatus),
		"Structured output":   descriptorValues(adapters, structuredOutputStatus),
		"MCP transports":      descriptorValues(adapters, mcpStatus),
	}
	for key, want := range expected {
		if got := capabilityStatus(t, body, key); !reflect.DeepEqual(got, want) {
			t.Errorf("README capability %q = %v, descriptor says %v", key, got, want)
		}
	}
}

func TestCapabilityDocumentMatchesDescriptors(t *testing.T) {
	root := repositoryRoot(t)
	body := readFile(t, filepath.Join(root, "docs", "capabilities.md"))
	adapters := []agentadaptor.DriverAdapter{codex.NewAdapter(), claude.NewAdapter(), cursor.NewAdapter()}

	expected := map[string][]string{
		"Default and named bindings":      {"yes", "yes", "yes"},
		"`Run` / `Start` lifecycle":       {"yes", "yes", "yes"},
		"Session resume":                  descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().Sessions.SupportsResume) }),
		"Result layers":                   {"yes", "yes", "yes"},
		"Workspace and profile selection": descriptorValues(adapters, workspaceProfileStatus),
		"Persistent skill sync": descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string {
			return yesNo(a.Descriptor().Skills.Supported && a.Descriptor().Skills.Mode == agentadaptor.SkillSyncPersistent)
		}),
		"Runtime-service reports":                descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().Runtime.ReportsServices) }),
		"Admin environment/models/schema/skills": descriptorValues(adapters, adminDiscoveryStatus),
		"Quota probe":                            descriptorValues(adapters, quotaStatus),
		"Token-level assistant text":             descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(streamCapability(a).TokenLevel) }),
		"Reasoning deltas":                       descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(streamCapability(a).Reasoning) }),
		"Tool-call argument deltas":              descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(streamCapability(a).ToolCallArgs) }),
		"HITL requested/resolved stream":         descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(streamCapability(a).HITL) }),
		"Permission `Ask`":                       descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().RunPolicyCaps.Permission.Ask) }),
		"PlanReview `Ask`":                       descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().RunPolicyCaps.PlanReview.Ask) }),
		"Question `Ask`":                         descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().RunPolicyCaps.Question.Ask) }),
		"Permission auto-approve": descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string {
			return yesNo(a.Descriptor().RunPolicyCaps.Permission.AutoApprove)
		}),
		"Auto-reject":     descriptorValues(adapters, autoRejectStatus),
		"Automatic retry": descriptorValues(adapters, retryStatus),
		"Native JSON Schema": descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string {
			return yesNo(a.Descriptor().StructuredOutput.JSONSchemaNative)
		}),
		"Prompt + local validation": descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string {
			return yesNo(a.Descriptor().StructuredOutput.JSONSchemaPromptValidate)
		}),
		"Works with `Run`": descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().StructuredOutput.WorksWithRun) }),
		"Works with `Start`": descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string {
			return yesNo(a.Descriptor().StructuredOutput.WorksWithStart)
		}),
		"Native schema + content streaming": descriptorValues(adapters, nativeSchemaStreamingStatus),
		"Structured output + HITL":          descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().StructuredOutput.WorksWithHITL) }),
		"MCP stdio":                         descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().MCP.Stdio) }),
		"MCP HTTP":                          descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().MCP.HTTP) }),
		"MCP SSE":                           descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().MCP.SSE) }),
		"Instructions":                      {"yes", "yes", "partial"},
		"Profile agents":                    {"partial", "partial", "partial"},
		"Hooks":                             {"partial", "partial", "partial"},
		"Profile config patches":            {"partial", "partial", "partial"},
		"Plugin abstraction":                {"no", "no", "no"},
		"SDK-managed environment bindings":  {"yes", "yes", "yes"},
		"Isolation policy descriptor":       descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().RunPolicyCaps.Isolation) }),
		"Web search policy":                 descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().RunPolicyCaps.WebSearch) }),
		"Browser policy":                    descriptorValues(adapters, func(a agentadaptor.DriverAdapter) string { return yesNo(a.Descriptor().RunPolicyCaps.Browser) }),
		"Protocol-owned checkpoint parsing": {"yes", "yes", "yes"},
	}
	for key, want := range expected {
		if got := capabilityStatus(t, body, key); !reflect.DeepEqual(got, want) {
			t.Errorf("capability document %q = %v, descriptor says %v", key, got, want)
		}
	}
}

func TestShowcaseReadmesHaveOperationalContract(t *testing.T) {
	root := repositoryRoot(t)
	showcases := []string{"managed-profile", "full-profile", "web-sse", "web-agui", "web-copilotkit-hitl", "a2a-local", "team-agent-workflow"}
	required := []string{"## Architecture", "## Prerequisites", "## Provider Support", "## Setup And Run", "## Expected Evidence", "## Cleanup", "## Security Notes", "## Known Limitations"}
	for _, name := range showcases {
		body := readFile(t, filepath.Join(root, "examples", "showcases", name, "README.md"))
		for _, heading := range required {
			if !strings.Contains(body, heading) {
				t.Errorf("showcase %s is missing %q", name, heading)
			}
		}
	}
}

func TestExampleCatalogCoversTargetSurface(t *testing.T) {
	root := repositoryRoot(t)
	english := readFile(t, filepath.Join(root, "examples/README.md"))
	chinese := readFile(t, filepath.Join(root, "examples/README.zh-CN.md"))
	required := []string{
		"recipes/basic-run",
		"recipes/provider-selection",
		"recipes/async-events",
		"recipes/content-streaming",
		"recipes/session-continuity",
		"recipes/named-agent-review",
		"recipes/admin-preflight",
		"recipes/result-and-failure",
		"recipes/structured-output",
		"recipes/hitl-handler",
		"recipes/hitl-channel",
		"recipes/skill-injection",
		"recipes/runtime-service",
		"recipes/custom-adapter",
		"showcases/managed-profile",
		"showcases/full-profile",
		"showcases/web-sse",
		"showcases/web-agui",
		"showcases/web-copilotkit-hitl",
		"showcases/a2a-local",
		"showcases/team-agent-workflow",
		"tools/session-codec-inspect",
		"tools/live-smoke",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(root, "examples", filepath.FromSlash(rel))); err != nil {
			t.Errorf("required example directory %s: %v", rel, err)
		}
		for _, doc := range []struct {
			name string
			body string
		}{{"examples/README.md", english}, {"examples/README.zh-CN.md", chinese}} {
			if !strings.Contains(doc.body, rel) {
				t.Errorf("%s does not catalog %s", doc.name, rel)
			}
		}
	}
}

func TestTeamAgentWorkflowKeepsMCPToA2AComposition(t *testing.T) {
	root := repositoryRoot(t)
	base := filepath.Join(root, "examples", "showcases", "team-agent-workflow")
	files := []string{"main.go", "roles.go", "delegation_runtime.go", "trace.go", "README.md"}
	var joined strings.Builder
	for _, name := range files {
		joined.WriteString(readFile(t, filepath.Join(base, name)))
		joined.WriteByte('\n')
	}
	body := joined.String()
	for _, required := range []string{
		"AgentClaude",
		"AgentCodex",
		"NewMCPServer",
		"NewRegistry",
		"bridgea2a.NewServer",
		"agentadaptor.mcp.enabled",
		"delegate_to_agent",
		"plan (Codex) -> impl (Claude Code) -> review (Codex)",
		"TEAM_AGENT_WORKFLOW_OK",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("team-agent-workflow is missing composition marker %q", required)
		}
	}
}

func TestExampleCatalogsMatchFilesystemAndContract(t *testing.T) {
	root := repositoryRoot(t)
	english := readFile(t, filepath.Join(root, "examples", "README.md"))
	chinese := readFile(t, filepath.Join(root, "examples", "README.zh-CN.md"))

	if got, want := sortedMatches(catalogPathPattern, english), sortedMatches(catalogPathPattern, chinese); !reflect.DeepEqual(got, want) {
		t.Fatalf("example catalog paths differ\nenglish: %v\nchinese: %v", got, want)
	}
	if got, want := sortedMatches(catalogCommandPattern, english), sortedMatches(catalogCommandPattern, chinese); !reflect.DeepEqual(got, want) {
		t.Fatalf("example catalog commands differ\nenglish: %v\nchinese: %v", got, want)
	}

	for _, category := range []string{"recipes", "showcases", "tools"} {
		entries, err := os.ReadDir(filepath.Join(root, "examples", category))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			rel := category + "/" + entry.Name()
			for _, doc := range []struct {
				name string
				body string
			}{{"examples/README.md", english}, {"examples/README.zh-CN.md", chinese}} {
				row := catalogRow(t, doc.name, doc.body, rel)
				if len(row) != 6 {
					t.Errorf("%s row for %s has %d fields, want 6", doc.name, rel, len(row))
				}
				for i, value := range row {
					if strings.TrimSpace(value) == "" {
						t.Errorf("%s row for %s has empty field %d", doc.name, rel, i)
					}
				}
			}
		}
	}

	allowed := map[string]bool{"internal": true, "recipes": true, "showcases": true, "tools": true}
	topLevel, err := os.ReadDir(filepath.Join(root, "examples"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range topLevel {
		if entry.IsDir() && !allowed[entry.Name()] {
			t.Errorf("examples contains uncategorized top-level directory %q", entry.Name())
		}
	}
}

func TestRecipesHaveTimeoutAndExplainLongEntries(t *testing.T) {
	root := repositoryRoot(t)
	english := readFile(t, filepath.Join(root, "examples", "README.md"))
	chinese := readFile(t, filepath.Join(root, "examples", "README.zh-CN.md"))
	entries, err := os.ReadDir(filepath.Join(root, "examples", "recipes"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rel := "recipes/" + entry.Name()
		source := readFile(t, filepath.Join(root, "examples", filepath.FromSlash(rel), "main.go"))
		if !strings.Contains(source, "context.WithTimeout") {
			t.Errorf("%s does not bound its run with context.WithTimeout", rel)
		}
		if lines := len(strings.Split(strings.TrimSuffix(source, "\n"), "\n")); lines > 120 {
			if !strings.Contains(strings.Join(catalogRow(t, "examples/README.md", english, rel), " "), "over 120") {
				t.Errorf("%s has %d lines without an English catalog rationale", rel, lines)
			}
			if !strings.Contains(strings.Join(catalogRow(t, "examples/README.zh-CN.md", chinese, rel), " "), "超 120") {
				t.Errorf("%s has %d lines without a Chinese catalog rationale", rel, lines)
			}
		}
	}
}

var publicMarkdownFiles = []string{
	"README.md",
	"README.zh-CN.md",
	"examples/README.md",
	"examples/README.zh-CN.md",
	"docs/README.md",
	"docs/api-reference.md",
	"docs/capabilities.md",
	"docs/usage-guide.md",
	"docs/run-policy.md",
	"docs/structured-output.md",
	"docs/streaming.md",
	"docs/a2a.md",
	"examples/showcases/managed-profile/README.md",
	"examples/showcases/full-profile/README.md",
	"examples/showcases/web-sse/README.md",
	"examples/showcases/web-agui/README.md",
	"examples/showcases/web-copilotkit-hitl/README.md",
	"examples/showcases/a2a-local/README.md",
	"examples/showcases/team-agent-workflow/README.md",
}

func descriptorValues(adapters []agentadaptor.DriverAdapter, status func(agentadaptor.DriverAdapter) string) []string {
	values := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		values = append(values, status(adapter))
	}
	return values
}

func sessionStatus(adapter agentadaptor.DriverAdapter) string {
	if adapter.Descriptor().Sessions.SupportsResume {
		return "yes"
	}
	return "no"
}

func streamingStatus(adapter agentadaptor.DriverAdapter) string {
	aware, ok := adapter.(interface {
		StreamCapability() agentadaptor.StreamCapability
	})
	if ok && aware.StreamCapability().Native && aware.StreamCapability().TokenLevel {
		return "native"
	}
	return "no"
}

func hitlAskStatus(adapter agentadaptor.DriverAdapter) string {
	caps := adapter.Descriptor().RunPolicyCaps
	values := make([]string, 0, 3)
	if caps.Permission.Ask {
		values = append(values, "Permission")
	}
	if caps.PlanReview.Ask {
		values = append(values, "PlanReview")
	}
	if caps.Question.Ask {
		values = append(values, "Question")
	}
	if len(values) == 0 {
		return "no"
	}
	return strings.Join(values, " + ")
}

func structuredOutputStatus(adapter agentadaptor.DriverAdapter) string {
	caps := adapter.Descriptor().StructuredOutput
	if caps.JSONSchemaNative && caps.JSONSchemaPromptValidate {
		return "native + validate"
	}
	if caps.JSONSchemaNative {
		return "native"
	}
	if caps.JSONSchemaPromptValidate {
		return "prompt + validate"
	}
	return "no"
}

func mcpStatus(adapter agentadaptor.DriverAdapter) string {
	caps := adapter.Descriptor().MCP
	values := make([]string, 0, 3)
	if caps.Stdio {
		values = append(values, "stdio")
	}
	if caps.HTTP {
		values = append(values, "HTTP")
	}
	if caps.SSE {
		values = append(values, "SSE")
	}
	if len(values) == 0 {
		return "no"
	}
	return strings.Join(values, " + ")
}

func streamCapability(adapter agentadaptor.DriverAdapter) agentadaptor.StreamCapability {
	aware, ok := adapter.(interface {
		StreamCapability() agentadaptor.StreamCapability
	})
	if !ok {
		return agentadaptor.StreamCapability{}
	}
	return aware.StreamCapability()
}

func workspaceProfileStatus(adapter agentadaptor.DriverAdapter) string {
	_, profileAware := adapter.(agentadaptor.ProfileAwareDriver)
	return yesNo(adapter.Descriptor().Workspace.Supported && profileAware)
}

func adminDiscoveryStatus(adapter agentadaptor.DriverAdapter) string {
	descriptor := adapter.Descriptor()
	_, environmentAware := adapter.(agentadaptor.EnvironmentAwareDriver)
	_, modelAware := adapter.(agentadaptor.ModelAwareDriver)
	_, skillAware := adapter.(agentadaptor.SkillAwareDriver)
	complete := environmentAware && modelAware && skillAware &&
		descriptor.ConfigSchema != nil && len(descriptor.Models) > 0 && descriptor.Skills.Supported
	if complete {
		return "yes"
	}
	return "partial"
}

func quotaStatus(adapter agentadaptor.DriverAdapter) string {
	if adapter.Descriptor().Type == cursor.DriverType {
		return "no"
	}
	if _, ok := adapter.(agentadaptor.QuotaAwareDriver); ok {
		return "conditional"
	}
	return "no"
}

func autoRejectStatus(adapter agentadaptor.DriverAdapter) string {
	caps := adapter.Descriptor().RunPolicyCaps
	return yesNo(caps.Permission.AutoReject || caps.PlanReview.AutoReject || caps.Question.AutoReject)
}

func retryStatus(adapter agentadaptor.DriverAdapter) string {
	caps := adapter.Descriptor().RunPolicyCaps
	return yesNo(caps.Permission.Retry || caps.PlanReview.Retry || caps.Question.Retry)
}

func nativeSchemaStreamingStatus(adapter agentadaptor.DriverAdapter) string {
	caps := adapter.Descriptor().StructuredOutput
	if !caps.JSONSchemaNative {
		return "n/a"
	}
	return yesNo(caps.WorksWithStreaming)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func checkLocalLink(t *testing.T, root, source, rawTarget string) {
	t.Helper()
	if rawTarget == "" || strings.HasPrefix(rawTarget, "#") && !strings.Contains(rawTarget, "/") {
		if strings.HasPrefix(rawTarget, "#") {
			checkAnchor(t, source, strings.TrimPrefix(rawTarget, "#"))
		}
		return
	}
	lower := strings.ToLower(rawTarget)
	for _, prefix := range []string{"http://", "https://", "mailto:", "data:"} {
		if strings.HasPrefix(lower, prefix) {
			return
		}
	}

	target := strings.Trim(rawTarget, "<>")
	pathPart, fragment, _ := strings.Cut(target, "#")
	pathPart, _, _ = strings.Cut(pathPart, "?")
	decoded, err := url.PathUnescape(pathPart)
	if err != nil {
		t.Errorf("%s has invalid escaped link %q: %v", source, rawTarget, err)
		return
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(source), filepath.FromSlash(decoded)))
	if rel, err := filepath.Rel(root, resolved); err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("%s links outside repository: %q", source, rawTarget)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil {
		t.Errorf("%s has unresolved link %q: %v", source, rawTarget, err)
		return
	}
	if fragment != "" && !info.IsDir() && strings.EqualFold(filepath.Ext(resolved), ".md") {
		checkAnchor(t, resolved, fragment)
	}
}

func checkAnchor(t *testing.T, path, rawAnchor string) {
	t.Helper()
	anchor, err := url.PathUnescape(rawAnchor)
	if err != nil {
		t.Errorf("%s has invalid anchor %q: %v", path, rawAnchor, err)
		return
	}
	body := readFile(t, path)
	anchors := markdownAnchors(body)
	if _, ok := anchors[anchor]; !ok {
		t.Errorf("%s has no Markdown heading for anchor #%s", path, anchor)
	}
}

func markdownAnchors(body string) map[string]struct{} {
	anchors := make(map[string]struct{})
	counts := make(map[string]int)
	for _, match := range headingPattern.FindAllStringSubmatch(markdownOutsideCode(body), -1) {
		base := githubSlug(match[1])
		if base == "" {
			continue
		}
		anchor := base
		if count := counts[base]; count > 0 {
			anchor += "-" + strconv.Itoa(count)
		}
		counts[base]++
		anchors[anchor] = struct{}{}
	}
	return anchors
}

func markdownOutsideCode(body string) string {
	var out strings.Builder
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			out.WriteByte('\n')
			continue
		}
		if inFence {
			out.WriteByte('\n')
			continue
		}

		inInlineCode := false
		for _, r := range line {
			if r == '`' {
				inInlineCode = !inInlineCode
				out.WriteRune(' ')
				continue
			}
			if inInlineCode {
				out.WriteRune(' ')
				continue
			}
			out.WriteRune(r)
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func githubSlug(value string) string {
	value = strings.ToLower(strings.ReplaceAll(value, "`", ""))
	var out strings.Builder
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r), r == '-', r == '_':
			out.WriteRune(r)
		case unicode.IsSpace(r):
			out.WriteByte('-')
		}
	}
	return strings.Trim(out.String(), "-")
}

func sortedMatches(pattern *regexp.Regexp, value string) []string {
	seen := make(map[string]struct{})
	for _, match := range pattern.FindAllString(value, -1) {
		seen[match] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for match := range seen {
		out = append(out, match)
	}
	sort.Strings(out)
	return out
}

func normalizedLocalLinks(body string) []string {
	seen := make(map[string]struct{})
	for _, match := range markdownLinkPattern.FindAllStringSubmatch(markdownOutsideCode(body), -1) {
		target := strings.Trim(strings.TrimSpace(match[1]), "<>")
		if !strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../") {
			continue
		}
		switch target {
		case "./README.md", "./README.zh-CN.md":
			target = "<root-language-switch>"
		case "./examples/README.md", "./examples/README.zh-CN.md":
			target = "<examples-catalog>"
		}
		seen[target] = struct{}{}
	}
	links := make([]string, 0, len(seen))
	for target := range seen {
		links = append(links, target)
	}
	sort.Strings(links)
	return links
}

func publicAPITokens(body string) []string {
	seen := make(map[string]struct{})
	for _, match := range inlineCodePattern.FindAllStringSubmatch(body, -1) {
		token := strings.TrimSpace(match[1])
		if !publicAPITokenPattern.MatchString(token) || !strings.ContainsAny(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			continue
		}
		seen[token] = struct{}{}
	}
	tokens := make([]string, 0, len(seen))
	for token := range seen {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens
}

func levelTwoHeadings(body string) []string {
	headings := make([]string, 0)
	for _, line := range strings.Split(markdownOutsideCode(body), "\n") {
		if strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "### ") {
			headings = append(headings, strings.TrimSpace(strings.TrimPrefix(line, "## ")))
		}
	}
	return headings
}

func sectionBulletCount(body, heading string) int {
	rest := sectionText(body, heading)
	count := 0
	for _, line := range strings.Split(markdownOutsideCode(rest), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			count++
		}
	}
	return count
}

func sectionText(body, heading string) string {
	start := strings.Index(body, heading)
	if start < 0 {
		return ""
	}
	rest := body[start+len(heading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func assertContainsAll(t *testing.T, label string, index int, body string, values []string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(body, value) {
			t.Errorf("%s semantic group %d is missing %q", label, index+1, value)
		}
	}
}

func fencedBlocks(body, language string) []string {
	marker := "```" + language + "\n"
	blocks := make([]string, 0)
	for {
		start := strings.Index(body, marker)
		if start < 0 {
			return blocks
		}
		body = body[start+len(marker):]
		end := strings.Index(body, "\n```")
		if end < 0 {
			return append(blocks, strings.TrimSpace(body))
		}
		blocks = append(blocks, strings.TrimSpace(body[:end]))
		body = body[end+len("\n```"):]
	}
}

func capabilityStatus(t *testing.T, body, key string) []string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		cells := strings.Split(strings.TrimSpace(line), "|")
		if len(cells) < 6 || strings.TrimSpace(cells[1]) != key {
			continue
		}
		return []string{
			strings.TrimSpace(cells[2]),
			strings.TrimSpace(cells[3]),
			strings.TrimSpace(cells[4]),
		}
	}
	t.Fatalf("Markdown is missing capability row %q", key)
	return nil
}

func catalogRow(t *testing.T, name, body, rel string) []string {
	t.Helper()
	needle := "](./" + rel + ")"
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		parts := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	t.Fatalf("%s has no catalog row for %s", name, rel)
	return nil
}

func firstGoBlockAfter(t *testing.T, body, heading string) string {
	t.Helper()
	start := strings.Index(body, heading)
	if start < 0 {
		t.Fatalf("missing heading %q", heading)
	}
	rest := body[start+len(heading):]
	open := strings.Index(rest, "```go\n")
	if open < 0 {
		t.Fatalf("heading %q has no Go block", heading)
	}
	rest = rest[open+len("```go\n"):]
	close := strings.Index(rest, "\n```")
	if close < 0 {
		t.Fatalf("heading %q has an unterminated Go block", heading)
	}
	return strings.TrimSpace(rest[:close])
}
