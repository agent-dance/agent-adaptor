package claude

import (
	"context"
	"regexp"

	"github.com/agent-dance/agent-adaptor/v2/internal/cliversion"
)

var versionProbe = cliversion.Probe{
	Pattern: regexp.MustCompile(`(?m)^[\t ]*v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)(?:[\t ]+\(Claude Code\))?[\t ]*\r?$`),
}

func claudeVersion(ctx context.Context, cfg Config) cliversion.Version {
	return versionProbe.Detect(ctx, cfg.CommonConfig, "claude")
}
