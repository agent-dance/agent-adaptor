package team_test

import (
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team/teamstoretest"
)

func TestMemoryStoreConformance(t *testing.T) {
	teamstoretest.Run(t, func(*testing.T) team.TaskStore { return team.NewMemoryStore() })
}
