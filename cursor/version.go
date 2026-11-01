package cursor

import (
	"context"
	"regexp"

	"github.com/agent-dance/agent-adaptor/v2/internal/cliversion"
)

var versionProbe = cliversion.Probe{
	Pattern: regexp.MustCompile(`(?m)^[\t ]*(?:(?:Cursor(?: Agent)?|[Aa]gent|cursor-agent)[\t ]+)?([0-9]{4}\.[0-9]{1,2}\.[0-9]{1,2}(?:-[0-9A-Za-z.-]+)?)[\t ]*\r?$`),
	Date:    true, // Cursor hashes identify builds of the same date, not prereleases.
}

func cursorVersion(ctx context.Context, cfg Config) cliversion.Version {
	return versionProbe.Detect(ctx, cfg.CommonConfig, "agent")
}
