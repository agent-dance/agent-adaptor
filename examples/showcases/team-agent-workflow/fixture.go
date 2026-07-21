package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type workflowFixture struct {
	RootDir      string
	WorkspaceDir string
	ProfilesDir  string
	Keep         bool
}

type workspaceValidation struct {
	Tests        string   `json:"tests"`
	DiffCheck    string   `json:"diff_check"`
	ChangedFiles []string `json:"changed_files"`
}

func newWorkflowFixture(keep bool) (*workflowFixture, error) {
	root, err := os.MkdirTemp("", "agent-adaptor-team-workflow-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary workflow root: %w", err)
	}
	fixture := &workflowFixture{
		RootDir:      root,
		WorkspaceDir: filepath.Join(root, "workspace"),
		ProfilesDir:  filepath.Join(root, "profiles"),
		Keep:         keep,
	}
	if err := os.MkdirAll(fixture.WorkspaceDir, 0o755); err != nil {
		fixture.Cleanup()
		return nil, fmt.Errorf("create workflow workspace: %w", err)
	}
	if err := os.MkdirAll(fixture.ProfilesDir, 0o700); err != nil {
		fixture.Cleanup()
		return nil, fmt.Errorf("create workflow profiles root: %w", err)
	}
	for name, body := range fixtureFiles() {
		if err := os.WriteFile(filepath.Join(fixture.WorkspaceDir, name), []byte(body), 0o644); err != nil {
			fixture.Cleanup()
			return nil, fmt.Errorf("write fixture %s: %w", name, err)
		}
	}
	if err := fixture.initializeGit(); err != nil {
		fixture.Cleanup()
		return nil, err
	}
	checkCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if output, err := runIn(checkCtx, fixture.WorkspaceDir, "go", "test", "./..."); err == nil {
		fixture.Cleanup()
		return nil, fmt.Errorf("fixture must start with a failing test, got success: %s", output)
	}
	return fixture, nil
}

func fixtureFiles() map[string]string {
	return map[string]string{
		"go.mod": "module example.com/team-workflow\n\ngo 1.23\n",
		"TASK.md": `# Task

Implement NormalizeSlug in slug.go.

Contract:

- lowercase ASCII letters;
- preserve ASCII digits;
- replace each run of other bytes with one hyphen;
- trim leading and trailing hyphens;
- return an empty string when no letters or digits remain;
- use only the Go standard library;
- modify only slug.go and do not commit.

Acceptance: go test ./... and git diff --check both pass.
`,
		"slug.go": `package teamworkflow

// NormalizeSlug converts a display label to an ASCII URL slug.
func NormalizeSlug(input string) string {
	return ""
}
`,
		"slug_test.go": `package teamworkflow

import "testing"

func TestNormalizeSlug(t *testing.T) {
	tests := map[string]string{
		"  Hello, Team Agents!  ": "hello-team-agents",
		"Go__SDK 2026":           "go-sdk-2026",
		"already-clean":          "already-clean",
		"Crème brûlée":           "cr-me-br-l-e",
		"A...B":                  "a-b",
		"-A-":                    "a",
		"---":                    "",
	}
	for input, want := range tests {
		if got := NormalizeSlug(input); got != want {
			t.Errorf("NormalizeSlug(%q) = %q, want %q", input, got, want)
		}
	}
}
`,
	}
}

func (f *workflowFixture) initializeGit() error {
	commands := [][]string{
		{"init", "-q"},
		{"add", "go.mod", "TASK.md", "slug.go", "slug_test.go"},
		{"-c", "user.name=agent-adaptor example", "-c", "user.email=example@invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "Initial failing fixture"},
	}
	for _, args := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		output, err := runIn(ctx, f.WorkspaceDir, "git", args...)
		cancel()
		if err != nil {
			return fmt.Errorf("initialize fixture git repository (%s): %w: %s", strings.Join(args, " "), err, output)
		}
	}
	return nil
}

func (f *workflowFixture) CloneProfileOption(role string) agentadaptor.AgentOption {
	return agentadaptor.WithCloneProfile(filepath.Join(f.ProfilesDir, role), agentadaptor.CloneProfileOptions{
		IncludeSettings: true,
		AuthMode:        agentadaptor.CloneProfileAuthLink,
	})
}

func (f *workflowFixture) Validate(ctx context.Context) (workspaceValidation, error) {
	validation := workspaceValidation{}
	testOutput, err := runIn(ctx, f.WorkspaceDir, "go", "test", "./...")
	if err != nil {
		return validation, fmt.Errorf("workspace tests failed: %w: %s", err, testOutput)
	}
	validation.Tests = strings.TrimSpace(testOutput)
	if validation.Tests == "" {
		validation.Tests = "passed"
	}

	diffOutput, err := runIn(ctx, f.WorkspaceDir, "git", "diff", "--check")
	if err != nil {
		return validation, fmt.Errorf("workspace diff check failed: %w: %s", err, diffOutput)
	}
	validation.DiffCheck = "passed"

	changedOutput, err := runRawIn(ctx, f.WorkspaceDir, "git", "-c", "status.renames=false", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return validation, fmt.Errorf("list workspace changes: %w: %s", err, changedOutput)
	}
	for _, record := range strings.Split(changedOutput, "\x00") {
		if record == "" {
			continue
		}
		if len(record) < 4 {
			return validation, fmt.Errorf("unexpected git status record %q", record)
		}
		if name := record[3:]; name != "" {
			validation.ChangedFiles = append(validation.ChangedFiles, name)
		}
	}
	sort.Strings(validation.ChangedFiles)
	if len(validation.ChangedFiles) != 1 || validation.ChangedFiles[0] != "slug.go" {
		return validation, fmt.Errorf("implementation must leave exactly slug.go modified, got %v", validation.ChangedFiles)
	}
	return validation, nil
}

func (f *workflowFixture) Digest() (string, error) {
	hash := sha256.New()
	err := filepath.WalkDir(f.WorkspaceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(f.WorkspaceDir, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if _, err := io.WriteString(hash, filepath.ToSlash(rel)+"\x00"); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, err = io.WriteString(hash, "symlink:"+target+"\x00")
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		_, err = io.WriteString(hash, "\x00")
		return err
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (f *workflowFixture) Cleanup() {
	if f != nil && !f.Keep && f.RootDir != "" {
		_ = os.RemoveAll(f.RootDir)
	}
}

func runIn(ctx context.Context, cwd, command string, args ...string) (string, error) {
	output, err := runRawIn(ctx, cwd, command, args...)
	return strings.TrimSpace(output), err
}

func runRawIn(ctx context.Context, cwd, command string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	return string(output), err
}
