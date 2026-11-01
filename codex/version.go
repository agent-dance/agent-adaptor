package codex

import (
	"context"
	"regexp"

	"github.com/agent-dance/agent-adaptor/v2/internal/cliversion"
)

var versionProbe = cliversion.Probe{
	Pattern: regexp.MustCompile(`(?m)^[\t ]*(?:codex(?:-cli)?[\t ]+)?v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)[\t ]*\r?$`),
}

func codexVersion(ctx context.Context, cfg Config) cliversion.Version {
	return versionProbe.Detect(ctx, cfg.CommonConfig, "codex")
}
