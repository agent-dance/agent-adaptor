package skill

import "github.com/agent-dance/agent-adaptor/internal/skillmaterializer"

// SkillCacheRootEnv names the AGENT_ADAPTOR_SKILL_CACHE_ROOT environment
// variable. It overrides the default materializer's cache root. Drivers use
// the same location to determine which materialized directories they may
// manage, so hosts should set it consistently for the whole process.
const SkillCacheRootEnv = "AGENT_ADAPTOR_SKILL_CACHE_ROOT"

// DefaultMaterializerOption configures the materializer returned by
// [NewDefaultSkillMaterializer].
type DefaultMaterializerOption func(*defaultMaterializerConfig)

type defaultMaterializerConfig struct {
	cacheRoot         string
	maxArchiveBytes   int64
	maxFileBytes      int64
	maxArchiveEntries int
}

// WithSkillCacheRoot overrides the cache root the default materializer
// writes to. Empty input falls back to [SkillCacheRootEnv], then
// os.UserCacheDir(), then os.TempDir().
func WithSkillCacheRoot(path string) DefaultMaterializerOption {
	return func(c *defaultMaterializerConfig) { c.cacheRoot = path }
}

// WithMaxArchiveSize caps the total compressed bytes the materializer
// reads from an [Archive] source. Streams that exceed the cap surface
// as an error before any extraction begins. Default 256 MiB; values
// <= 0 are treated as the default.
func WithMaxArchiveSize(bytes int64) DefaultMaterializerOption {
	return func(c *defaultMaterializerConfig) { c.maxArchiveBytes = bytes }
}

// WithMaxFileSize caps the uncompressed bytes a single archive entry
// may occupy. Decompression bombs surface as an error mid-extraction
// and the partially extracted temporary directory is removed. Default
// 64 MiB; values <= 0 are treated as the default.
func WithMaxFileSize(bytes int64) DefaultMaterializerOption {
	return func(c *defaultMaterializerConfig) { c.maxFileBytes = bytes }
}

// WithMaxArchiveEntries caps the number of entries (files + dirs) in a
// single archive. Archives with more entries surface as an error
// before any file is written. Default 10000; values <= 0 are treated
// as the default.
func WithMaxArchiveEntries(n int) DefaultMaterializerOption {
	return func(c *defaultMaterializerConfig) { c.maxArchiveEntries = n }
}

// NewDefaultSkillMaterializer returns the SDK's built-in materializer,
// which handles the sources produced by [Dir], [FS], [Inline], and
// [Archive]. Agents install it automatically; construct one explicitly
// only to tune the options above or to compose a
// chain-of-responsibility around it:
//
//	type storeMaterializer struct {
//	    fallback skill.Materializer
//	    store    *MyStoreClient
//	}
//
//	func (m *storeMaterializer) Materialize(ctx context.Context, s skill.Skill) (string, error) {
//	    if src, ok := s.Source.(myCustomSource); ok {
//	        return m.store.Fetch(ctx, src)
//	    }
//	    return m.fallback.Materialize(ctx, s)
//	}
func NewDefaultSkillMaterializer(opts ...DefaultMaterializerOption) Materializer {
	var cfg defaultMaterializerConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	materializerOpts := make([]skillmaterializer.Option, 0, 4)
	if cfg.cacheRoot != "" {
		materializerOpts = append(materializerOpts, skillmaterializer.WithSkillCacheRoot(cfg.cacheRoot))
	}
	if cfg.maxArchiveBytes > 0 {
		materializerOpts = append(materializerOpts, skillmaterializer.WithMaxArchiveSize(cfg.maxArchiveBytes))
	}
	if cfg.maxFileBytes > 0 {
		materializerOpts = append(materializerOpts, skillmaterializer.WithMaxFileSize(cfg.maxFileBytes))
	}
	if cfg.maxArchiveEntries > 0 {
		materializerOpts = append(materializerOpts, skillmaterializer.WithMaxArchiveEntries(cfg.maxArchiveEntries))
	}
	return skillmaterializer.New(ErrSkillSourceMissing, materializerOpts...)
}
