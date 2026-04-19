package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/examples/internal/mockkit"
)

const (
	proofSkillName    = "write-proof"
	proofExpectedText = "WRITE_PROOF_OK"
)

func main() {
	model := flag.String("model", "gpt-5.4", "Codex model to use")
	command := flag.String("command", "", "Optional explicit Codex-compatible command to run. Defaults to a bundled verifier that validates runtime skill injection.")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for the skills example")
	keepWorkspace := flag.Bool("keep-workspace", false, "Keep the temporary workspace after the example finishes")
	flag.Parse()

	workspaceDir, err := os.MkdirTemp("", "agent-adaptor-codex-skills-*")
	exampleutil.Must(err, "create temporary workspace")
	cleanup := !*keepWorkspace
	defer func() {
		if cleanup {
			_ = os.RemoveAll(workspaceDir)
		}
	}()

	skillDir := locateWriteProofSkill()
	proofPath := filepath.Join(workspaceDir, "proof.txt")
	commandPath, commandMode, commandNote, commandCleanup := prepareCodexCommand(*command)
	defer commandCleanup()

	skillCatalog := mockkit.StaticSkillCatalog{
		Entries: map[string]agentadaptor.Skill{
			proofSkillName: {
				Key:      proofSkillName,
				Runtime:  proofSkillName,
				PathHint: skillDir,
			},
			"shadow-unused": {
				Key:      "shadow-unused",
				Runtime:  "shadow-unused",
				Content:  "# shadow-unused\nThis skill is intentionally unused in the live example.",
				PathHint: skillDir,
			},
		},
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(
			agentadaptor.CodexConfig{
				CommonConfig: agentadaptor.CommonConfig{
					CWD:             workspaceDir,
					AgentProfileDir: "C:\\Users\\buthim\\Documents\\codex-shadow",
					Command:         commandPath,
				},
				Model: *model,
			},
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:       "skills-agent",
				TenantID: "examples",
				Name:     "skills-live",
			}),
			agentadaptor.WithDefaultSkills(proofSkillName, "shadow-unused"),
		)),
		agentadaptor.WithSkillCatalog(skillCatalog),
		agentadaptor.WithSkillAssembler(liveSkillAssembler{}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	defaultAdmin := sdk.Admin().Default()
	listedSkills, err := defaultAdmin.ListSkills(ctx)
	exampleutil.Must(err, "list default skills")
	exampleutil.Check(listedSkills.Supported, "expected listed skills to be supported")
	exampleutil.Check(len(listedSkills.Desired) == 2, "expected two default desired skills, got %d", len(listedSkills.Desired))

	syncedSkills, err := defaultAdmin.SyncSkills(ctx, []string{proofSkillName})
	exampleutil.Must(err, "sync default skills")
	exampleutil.Check(len(syncedSkills.Desired) == 1 && syncedSkills.Desired[0] == proofSkillName, "expected synced skills to contain only %q, got %#v", proofSkillName, syncedSkills.Desired)

	prompt := "Use the write-proof skill. Create the file at " + filepath.ToSlash(proofPath) +
		" with exactly this content: " + proofExpectedText + ". Do not modify any other files."
	result, err := sdk.Run(ctx, prompt, agentadaptor.WithSkills(proofSkillName))
	exampleutil.Must(err, "run codex skills-live example")
	exampleutil.Check(result.DriverType == codex.DriverType, "expected driver type %q, got %q", codex.DriverType, result.DriverType)
	exampleutil.Check(result.ExitCode == 0, "expected exit code 0, got %d", result.ExitCode)

	content, err := os.ReadFile(proofPath)
	exampleutil.Must(err, "read proof output %q; this example assumes Codex runtime skills injection already exists", proofPath)
	exampleutil.Check(strings.TrimSpace(string(content)) == proofExpectedText, "expected proof file content %q, got %q", proofExpectedText, strings.TrimSpace(string(content)))

	exampleutil.PrintJSON(map[string]any{
		"example":      "codex-skills-live",
		"verification": "Confirmed runtime skill injection by running the Codex adapter against a codex-compatible verifier that requires CODEX_HOME/skills/write-proof to exist before it will create the proof file.",
		"workspace":    workspaceDir,
		"command": map[string]any{
			"path": commandPath,
			"mode": commandMode,
			"note": commandNote,
		},
		"proof": map[string]any{
			"path":     proofPath,
			"contents": strings.TrimSpace(string(content)),
		},
		"list_skills": listedSkills,
		"sync_skills": syncedSkills,
		"run_result":  result,
	})
}

func locateWriteProofSkill() string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "locate current example source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "internal", "skills", "write-proof"))
}

func locateRepoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "locate current example source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func prepareCodexCommand(override string) (string, string, string, func()) {
	if strings.TrimSpace(override) != "" {
		command, note := exampleutil.RequireHealthyCodexCommand(override)
		return command, "external", note, func() {}
	}

	repoRoot := locateRepoRoot()
	tempDir, err := os.MkdirTemp("", "agent-adaptor-mock-codex-*")
	exampleutil.Must(err, "create temporary codex verifier directory")
	commandPath := filepath.Join(tempDir, "mock-codex")
	if runtime.GOOS == "windows" {
		commandPath += ".exe"
	}

	buildCmd := exec.Command("go", "build", "-o", commandPath, "./examples/internal/cmd/mock-codex")
	buildCmd.Dir = repoRoot
	buildCmd.Env = ensureGoBuildEnv(os.Environ(), filepath.Join(repoRoot, ".gocache"))
	output, err := buildCmd.CombinedOutput()
	exampleutil.Must(err, "build bundled codex verifier: %s", strings.TrimSpace(string(output)))

	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}
	return commandPath, "bundled-verifier", "Using the bundled verifier by default so the skills-live example remains deterministic and only validates runtime skill injection.", cleanup
}

func ensureGoBuildEnv(base []string, fallbackCache string) []string {
	base = exampleutil.EnsureWindowsProcessEnv(base)
	if os.Getenv("GOCACHE") != "" {
		return base
	}
	cacheRoot := fallbackCache
	if strings.TrimSpace(cacheRoot) == "" {
		cacheRoot = filepath.Join(os.TempDir(), "agent-adaptor-gocache")
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		exampleutil.Fatalf("create go build cache %q: %v", cacheRoot, err)
	}
	return append(base, fmt.Sprintf("GOCACHE=%s", cacheRoot))
}

type liveSkillAssembler struct{}

func (liveSkillAssembler) Prepare(_ context.Context, req agentadaptor.SkillAssemblyRequest) (agentadaptor.SkillPayload, error) {
	runtimeEntries := make([]agentadaptor.SkillRuntimeEntry, 0, len(req.Resolved))
	for _, skill := range req.Resolved {
		runtimeName := strings.TrimSpace(skill.Runtime)
		if runtimeName == "" {
			runtimeName = skill.Key
		}
		runtimeEntries = append(runtimeEntries, agentadaptor.SkillRuntimeEntry{
			Key:            skill.Key,
			RuntimeName:    runtimeName,
			SourcePath:     skill.PathHint,
			Required:       skill.Required,
			RequiredReason: skill.RequiredReason,
		})
	}

	payload := agentadaptor.SkillPayload{
		Mode:           agentadaptor.SkillSyncEphemeral,
		Requested:      append([]string(nil), req.Requested...),
		Resolved:       cloneLiveSkills(req.Resolved),
		RuntimeEntries: runtimeEntries,
	}
	payload.Fingerprint = liveSkillFingerprint(payload)
	return payload, nil
}

func cloneLiveSkills(skills []agentadaptor.Skill) []agentadaptor.Skill {
	if len(skills) == 0 {
		return nil
	}
	out := make([]agentadaptor.Skill, len(skills))
	copy(out, skills)
	return out
}

func liveSkillFingerprint(payload agentadaptor.SkillPayload) string {
	raw, err := json.Marshal(payload)
	exampleutil.Must(err, "marshal live skill payload fingerprint input")
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:8])
}
