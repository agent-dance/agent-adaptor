// Package skill is the consumer-facing vocabulary for agent skills.
//
// A skill is a directory containing SKILL.md (plus optional reference
// files) that teaches an agent a repeatable workflow. This package
// provides constructors that produce [Ref] values accepted by
// adaptor.WithSkills:
//
//	adaptor.WithSkills(
//	    skill.Dir("./skills/write-proof"),         // local directory
//	    skill.Key("code-review"),                  // provider-side catalogue key
//	    skill.Inline("greet", "# Greeting\n..."),  // literal SKILL.md content
//	    skill.Archive("kit", skill.ArchiveFile("./kit.tgz")), // zip / tar / tar.gz bundle
//	)
//
// [Dir], [FS], [Inline], and [Archive] build self-contained skill
// definitions; [Key] references a skill resolved at run time by the
// host-installed [Provider]. [Provider] and [Materializer] are the two
// host extension points: a Provider translates catalogue keys into
// concrete skill definitions (and may inject tenant-mandatory required
// skills), while a Materializer controls how skill sources are written
// to disk before a driver consumes them.
//
// This package also owns the host extension contracts, source values, default
// materializer, and skill-specific error identities used by errors.Is and
// errors.As.
package skill
