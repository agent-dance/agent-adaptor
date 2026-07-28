package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DirScanOption configures LocalSkillsFromDir. Hosts that need
// non-default behaviour (custom key prefix, exact-name exclusions, custom
// SKILL.md filename) chain options into the call.
type DirScanOption func(*dirScanConfig)

type dirScanConfig struct {
	keyPrefix      string
	skillFile      string
	ignoreEntry    map[string]struct{}
	requireSkillMd bool
}

func defaultDirScanConfig() dirScanConfig {
	return dirScanConfig{
		skillFile:      "SKILL.md",
		ignoreEntry:    map[string]struct{}{},
		requireSkillMd: true,
	}
}

// WithDirSkillKeyPrefix prepends prefix to every Skill.Key produced
// by the scan. A prefix of "team/" turns a directory named
// "code-review" into key "team/code-review". Prefixes that already
// end with "/" are honoured verbatim; other prefixes get a "/"
// separator appended.
//
// Empty prefix is the default and produces bare directory names.
func WithDirSkillKeyPrefix(prefix string) DirScanOption {
	return func(c *dirScanConfig) {
		trimmed := strings.TrimSpace(prefix)
		if trimmed == "" {
			c.keyPrefix = ""
			return
		}
		if !strings.HasSuffix(trimmed, "/") {
			trimmed += "/"
		}
		c.keyPrefix = trimmed
	}
}

// WithDirIgnore declares directory names that the scan must skip.
// Useful for filtering generated or dependency directories such as
// "node_modules".
//
// Multiple WithDirIgnore options accumulate; matching is exact
// (case-sensitive), not glob-style.
func WithDirIgnore(names ...string) DirScanOption {
	return func(c *dirScanConfig) {
		if c.ignoreEntry == nil {
			c.ignoreEntry = map[string]struct{}{}
		}
		for _, name := range names {
			trimmed := strings.TrimSpace(name)
			if trimmed == "" {
				continue
			}
			c.ignoreEntry[trimmed] = struct{}{}
		}
	}
}

// WithDirSkillFile overrides the per-directory marker file name.
// Default "SKILL.md". Setting it to, for example, "AGENT.md" lets the scan
// identify directories that follow a different convention. This option only
// changes discovery; callers remain responsible for ensuring each returned
// directory has the files required by its eventual consumer.
func WithDirSkillFile(name string) DirScanOption {
	return func(c *dirScanConfig) {
		if name = strings.TrimSpace(name); name != "" {
			c.skillFile = name
		}
	}
}

// LocalSkillsFromDir scans root and produces one Skill per
// subdirectory that contains the SKILL marker file (default
// "SKILL.md"). The scan is deterministic: subdirectories are
// processed in lexical order, so the returned slice is stable across
// runs on the same filesystem.
//
// Each produced Skill has:
//
//   - Key: directory basename, optionally prefixed via WithDirSkillKeyPrefix
//   - Source: the same local-directory source [Dir] builds, pointing at
//     the absolute path of the subdirectory
//   - Required / Reason / Metadata: zero values (the scan does NOT
//     parse SKILL.md frontmatter; hosts that need that should
//     post-process the slice)
//
// Subdirectories without a SKILL marker file are silently skipped so
// the scan tolerates a mixed root (some skills, some plain
// directories). Hidden entries (starting with ".") are skipped by
// default; pass WithDirIgnore to skip additional names.
//
// Error semantics:
//
//   - root doesn't exist or isn't a directory → error
//   - root is unreadable (permission) → error
//   - individual subdirectory unreadable → error (better to fail
//     loudly than silently miss a skill the host expected)
//
// The returned []Skill feeds the skill options via SkillsAsRefs:
//
//	skills, err := skill.LocalSkillsFromDir("/opt/skills")
//	if err != nil { return err }
//	agent := adaptor.New(drv,
//	    adaptor.WithSkills(skill.SkillsAsRefs(skills)...),
//	)
func LocalSkillsFromDir(root string, opts ...DirScanOption) ([]Skill, error) {
	cfg := defaultDirScanConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	cleaned := strings.TrimSpace(root)
	if cleaned == "" {
		return nil, errors.New("agentadaptor: LocalSkillsFromDir: empty root")
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return nil, fmt.Errorf("agentadaptor: LocalSkillsFromDir: resolve %q: %w", cleaned, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("agentadaptor: LocalSkillsFromDir: stat %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("agentadaptor: LocalSkillsFromDir: %q is not a directory", abs)
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("agentadaptor: LocalSkillsFromDir: read %q: %w", abs, err)
	}

	out := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(name, ".") {
			continue
		}
		if _, ignored := cfg.ignoreEntry[name]; ignored {
			continue
		}
		subdir := filepath.Join(abs, name)
		marker := filepath.Join(subdir, cfg.skillFile)
		stat, err := os.Stat(marker)
		if err != nil {
			if os.IsNotExist(err) && cfg.requireSkillMd {
				continue
			}
			return nil, fmt.Errorf("agentadaptor: LocalSkillsFromDir: stat %q: %w", marker, err)
		}
		if stat.IsDir() {
			continue
		}
		out = append(out, Skill{
			Key:    cfg.keyPrefix + name,
			Source: PathSource{Path: subdir},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// SkillsAsRefs converts a []Skill into a []Ref so hosts can pass a
// scanned skill set into variadic options:
//
//	adaptor.WithSkills(skill.SkillsAsRefs(skills)...)
//
// The conversion preserves order and does not deep-clone nested values such
// as Metadata.
func SkillsAsRefs(skills []Skill) []Ref {
	if len(skills) == 0 {
		return nil
	}
	out := make([]Ref, len(skills))
	for i, s := range skills {
		out[i] = s
	}
	return out
}
