package skill

import "github.com/agent-dance/agent-adaptor/internal/engine"

// SkillCacheRootEnv is the environment variable that overrides the
// default materializer's cache root. Drivers inspect the same variable
// to decide which materialized skill directories they are allowed to
// manage, so hosts that relocate the cache should set it once for the
// whole process rather than per materializer.
const SkillCacheRootEnv = engine.SkillCacheRootEnv

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
// and the partially-extracted staging directory is removed. Default
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
	engineOpts := make([]engine.DefaultMaterializerOption, 0, 4)
	if cfg.cacheRoot != "" {
		engineOpts = append(engineOpts, engine.WithSkillCacheRoot(cfg.cacheRoot))
	}
	if cfg.maxArchiveBytes > 0 {
		engineOpts = append(engineOpts, engine.WithMaxArchiveSize(cfg.maxArchiveBytes))
	}
	if cfg.maxFileBytes > 0 {
		engineOpts = append(engineOpts, engine.WithMaxFileSize(cfg.maxFileBytes))
	}
	if cfg.maxArchiveEntries > 0 {
		engineOpts = append(engineOpts, engine.WithMaxArchiveEntries(cfg.maxArchiveEntries))
	}
	return engine.NewDefaultSkillMaterializer(engineOpts...)
}
