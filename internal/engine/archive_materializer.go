package engine

import (
	"github.com/agent-dance/agent-adaptor/internal/skillmaterializer"
	"github.com/agent-dance/agent-adaptor/skill"
)

// DefaultMaterializerOption remains available to the legacy root during the v1
// cutover. The option and implementation have one private source of truth.
type DefaultMaterializerOption = skillmaterializer.Option

func WithSkillCacheRoot(path string) DefaultMaterializerOption {
	return skillmaterializer.WithSkillCacheRoot(path)
}

func WithMaxArchiveSize(bytes int64) DefaultMaterializerOption {
	return skillmaterializer.WithMaxArchiveSize(bytes)
}

func WithMaxFileSize(bytes int64) DefaultMaterializerOption {
	return skillmaterializer.WithMaxFileSize(bytes)
}

func WithMaxArchiveEntries(n int) DefaultMaterializerOption {
	return skillmaterializer.WithMaxArchiveEntries(n)
}

func NewDefaultSkillMaterializer(opts ...DefaultMaterializerOption) SkillMaterializer {
	return skillmaterializer.New(skill.ErrSkillSourceMissing, opts...)
}

// The following private compatibility shapes keep the engine's historical
// archive fuzz corpus focused on the same single implementation.
type materializerConfig struct {
	cacheRoot         string
	maxArchiveBytes   int64
	maxFileBytes      int64
	maxArchiveEntries int
}

func defaultMaterializerConfig() materializerConfig {
	return materializerConfig{
		maxArchiveBytes:   256 << 20,
		maxFileBytes:      64 << 20,
		maxArchiveEntries: 10000,
	}
}

type archiveExtraction struct {
	Files map[string][]byte
}

func extractArchive(raw []byte, format SkillArchiveFormat, subpath string, cfg materializerConfig) (archiveExtraction, error) {
	extracted, err := skillmaterializer.ExtractArchive(raw, skillmaterializer.Format(format), subpath, skillmaterializer.Config{
		CacheRoot:         cfg.cacheRoot,
		MaxArchiveBytes:   cfg.maxArchiveBytes,
		MaxFileBytes:      cfg.maxFileBytes,
		MaxArchiveEntries: cfg.maxArchiveEntries,
		SourceMissing:     skill.ErrSkillSourceMissing,
	})
	return archiveExtraction{Files: extracted.Files}, err
}
