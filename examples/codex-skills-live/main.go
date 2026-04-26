package main

import (
	"context"
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

	// Dedicated profile dir must be a real path on the current machine — do
	// NOT hardcode an absolute path here. Creating it under workspaceDir
	// keeps the example portable (works on Linux/macOS/Windows), isolated
	// (no pollution of the user's real CODEX_HOME), and automatically
	// cleaned up with the rest of the workspace.
	dedicatedProfileDir := filepath.Join(workspaceDir, "codex-shadow")
	exampleutil.Must(os.MkdirAll(dedicatedProfileDir, 0o755), "create dedicated codex profile dir")

	skillSet := agentadaptor.SkillSet{
		proofSkillName: {
			Key:    proofSkillName,
			Source: agentadaptor.SkillFromPath{Path: skillDir},
		},
		"shadow-unused": mockkit.InlineSkill("shadow-unused", "# shadow-unused\nThis skill is intentionally unused in the live example."),
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(
			agentadaptor.CodexConfig{
				CommonConfig: agentadaptor.CommonConfig{
					CWD:     workspaceDir,
					Command: commandPath,
				},
				Model: *model,
			},
			agentadaptor.WithDedicatedProfile(dedicatedProfileDir),
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:       "skills-agent",
				TenantID: "examples",
				Name:     "skills-live",
			}),
			agentadaptor.WithDefaultSkills(agentadaptor.Key(proofSkillName), agentadaptor.Key("shadow-unused")),
		)),
		agentadaptor.WithSkillSet(skillSet),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	defaultAdmin := sdk.Admin().Default()
	listedSkills, err := defaultAdmin.ListSkills(ctx)
	exampleutil.Must(err, "list default skills")
	exampleutil.Check(listedSkills.Supported, "expected listed skills to be supported")
	exampleutil.Check(len(listedSkills.Selected) == 2, "expected two default selected skills, got %d", len(listedSkills.Selected))

	selectedSkills, err := defaultAdmin.SetSelectedSkills(ctx, []string{proofSkillName})
	exampleutil.Must(err, "set selected default skills")
	exampleutil.Check(len(selectedSkills.Selected) == 1 && selectedSkills.Selected[0] == proofSkillName, "expected selected skills to contain only %q, got %#v", proofSkillName, selectedSkills.Selected)

	prompt := "Use the write-proof skill. Create the file at " + filepath.ToSlash(proofPath) +
		" with exactly this content: " + proofExpectedText + ". Do not modify any other files."
	result, err := sdk.Run(ctx, prompt, agentadaptor.WithSkills(agentadaptor.Key(proofSkillName)))
	exampleutil.Must(err, "run codex skills-live example")
	exampleutil.Check(result.DriverType == codex.DriverType, "expected driver type %q, got %q", codex.DriverType, result.DriverType)
	exampleutil.Check(result.ExitCode == 0, "expected exit code 0, got %d", result.ExitCode)

	content, err := os.ReadFile(proofPath)
	exampleutil.Must(err, "read proof output %q; this example assumes Codex runtime skills injection already exists", proofPath)
	exampleutil.Check(strings.TrimSpace(string(content)) == proofExpectedText, "expected proof file content %q, got %q", proofExpectedText, strings.TrimSpace(string(content)))

	exampleutil.PrintJSON(map[string]any{
		"example":           "codex-skills-live",
		"verification":      "Confirmed runtime skill injection by running the Codex adapter against a codex-compatible verifier that requires CODEX_HOME/skills/write-proof to exist before it will create the proof file.",
		"workspace":         workspaceDir,
		"dedicated_profile": dedicatedProfileDir,
		"command": map[string]any{
			"path": commandPath,
			"mode": commandMode,
			"note": commandNote,
		},
		"proof": map[string]any{
			"path":     proofPath,
			"contents": strings.TrimSpace(string(content)),
		},
		"list_skills":         listedSkills,
		"set_selected_skills": selectedSkills,
		"run_result":          result,
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

var _ = context.TODO // keep context import stable for future extensions
