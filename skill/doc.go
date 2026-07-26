// Package skill is the consumer-facing vocabulary for agent skills.
//
// A skill is a directory containing SKILL.md (plus optional reference
// files) that teaches an agent a repeatable workflow. This package
// provides one-line constructors that produce values accepted by
// WithSkills / WithDefaultSkills as [Ref]:
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
// Every exported type here is an alias for the corresponding public
// type in github.com/agent-dance/agent-adaptor, so values built with
// this package interoperate 1:1 with the root-package skill API and
// with host code that predates this vocabulary package.
package skill
