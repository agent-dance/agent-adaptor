package agentadaptor

// Thin shell preserving the historical root-package directory-scan surface.
// The truth moved to the skill/ vocabulary package in P5.2; the option type
// is an alias and the two entry points delegate, so behaviour (including
// error text) is unchanged.

import "github.com/agent-dance/agent-adaptor/skill"

// DirScanOption configures LocalSkillsFromDir. Hosts that need
// non-default behaviour (custom key prefix, glob exclusions, custom
// SKILL.md filename) chain options into the call.
type DirScanOption = skill.DirScanOption

// WithDirSkillKeyPrefix prepends prefix to every Skill.Key produced
// by the scan. A prefix of "team/" turns a directory named
// "code-review" into key "team/code-review". Prefixes that already
// end with "/" are honoured verbatim; other prefixes get a "/"
// separator appended.
//
// Empty prefix is the default and produces bare directory names.
func WithDirSkillKeyPrefix(prefix string) DirScanOption {
	return skill.WithDirSkillKeyPrefix(prefix)
}

// WithDirIgnore declares directory names that the scan must skip.
// Useful for filtering ".git", "node_modules", or stray top-level
// files that look like skills but aren't.
//
// Multiple WithDirIgnore options accumulate; matching is exact
// (case-sensitive), not glob-style.
func WithDirIgnore(names ...string) DirScanOption {
	return skill.WithDirIgnore(names...)
}

// WithDirSkillFile overrides the per-directory marker file name.
// Default "SKILL.md". Setting it to e.g. "AGENT.md" lets the scan
// pick up directories that follow a different convention.
func WithDirSkillFile(name string) DirScanOption {
	return skill.WithDirSkillFile(name)
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
//   - Source: SkillFromPath{Path: <absolute path to the subdirectory>}
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
// The returned []Skill feeds WithDefaultSkills / WithSkills via
// SkillsAsRefs:
//
//	skills, err := agentadaptor.LocalSkillsFromDir("/opt/skills")
//	if err != nil { return err }
//	sdk := agentadaptor.New(
//	    agentadaptor.WithDefaultAgent(...),
//	    agentadaptor.WithDefaultSkills(agentadaptor.SkillsAsRefs(skills)...),
//	)
func LocalSkillsFromDir(root string, opts ...DirScanOption) ([]Skill, error) {
	return skill.LocalSkillsFromDir(root, opts...)
}

// SkillsAsRefs converts a []Skill into a []SkillRef so hosts can
// pass a scanned skill set into variadic options:
//
//	sdk.WithDefaultSkills(agentadaptor.SkillsAsRefs(skills)...)
//
// The conversion is shallow; the returned refs reference the same
// underlying Skill values.
func SkillsAsRefs(skills []Skill) []SkillRef {
	return skill.SkillsAsRefs(skills)
}
