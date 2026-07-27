package agentadaptor

// Thin shell preserving the historical root-package materializer surface.
// The zip/tar/tgz extraction machinery and the option family moved to
// internal/engine in P5.2 (runtime machinery, not vocabulary); the option
// type is an alias and the constructors delegate, so behaviour is unchanged.

import "github.com/agent-dance/agent-adaptor/internal/engine"

// DefaultMaterializerOption configures the materializer returned by
// NewDefaultSkillMaterializer.
type DefaultMaterializerOption = engine.DefaultMaterializerOption

// WithSkillCacheRoot overrides the cache root the default materializer
// writes to. Empty input falls back to the AGENT_ADAPTOR_SKILL_CACHE_ROOT
// env var, then os.UserCacheDir(), then os.TempDir().
func WithSkillCacheRoot(path string) DefaultMaterializerOption {
	return engine.WithSkillCacheRoot(path)
}

// WithMaxArchiveSize caps the total compressed bytes the materializer
// will read from a SkillFromArchive stream. Streams that exceed the cap
// surface as an error before any extraction begins. Default 256 MiB.
// Values <= 0 are treated as the default.
func WithMaxArchiveSize(bytes int64) DefaultMaterializerOption {
	return engine.WithMaxArchiveSize(bytes)
}

// WithMaxFileSize caps the uncompressed bytes a single archive entry is
// allowed to occupy. Decompression bombs that inflate one entry past
// the cap surface as an error mid-extraction; partially-extracted
// staging directories are cleaned up. Default 64 MiB. Values <= 0 are
// treated as the default.
func WithMaxFileSize(bytes int64) DefaultMaterializerOption {
	return engine.WithMaxFileSize(bytes)
}

// WithMaxArchiveEntries caps the number of entries (files + dirs) in a
// single archive. Archives with more entries than the cap surface as
// an error before any file is written. Default 10000. Values <= 0 are
// treated as the default.
func WithMaxArchiveEntries(n int) DefaultMaterializerOption {
	return engine.WithMaxArchiveEntries(n)
}

// NewDefaultSkillMaterializer returns SDK's built-in materializer
// handling SkillFromPath / SkillFromFS / SkillFromInline / SkillFromArchive.
// Hosts that need to support custom SkillSource types should compose a
// chain-of-responsibility:
//
//	type StoreMaterializer struct {
//	    Default agentadaptor.SkillMaterializer
//	    Store   *MyStoreClient
//	}
//
//	func (m *StoreMaterializer) Materialize(ctx context.Context, s agentadaptor.Skill) (string, error) {
//	    if src, ok := s.Source.(MyCustomSource); ok {
//	        return m.Store.Fetch(ctx, src)
//	    }
//	    return m.Default.Materialize(ctx, s)
//	}
//
//	sdk := agentadaptor.New(
//	    agentadaptor.WithDefaultAgent(...),
//	    agentadaptor.WithSkillMaterializer(&StoreMaterializer{
//	        Default: agentadaptor.NewDefaultSkillMaterializer(),
//	        Store:   client,
//	    }),
//	)
func NewDefaultSkillMaterializer(opts ...DefaultMaterializerOption) SkillMaterializer {
	return engine.NewDefaultSkillMaterializer(opts...)
}
