package codebuddy

import (
	"context"
	"regexp"

	"github.com/agent-dance/agent-adaptor/v2/internal/cliversion"
)

var versionProbe = cliversion.Probe{
	Pattern: regexp.MustCompile(`(?m)^[\t ]*(?:(?:[Cc]ode[Bb]uddy(?:-cli)?)[\t ]+)?v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)(?:[\t ]+\(CodeBuddy\))?[\t ]*\r?$`),
}

func codebuddyVersion(ctx context.Context, cfg Config) cliversion.Version {
	return versionProbe.Detect(ctx, cfg.CommonConfig, defaultCommand)
}
