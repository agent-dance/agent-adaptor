//go:build e2e

package e2e_test

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cucumber/godog"
)

const defaultE2ETags = "@smoke&&@journey"

var executedScenarios atomic.Int64

func TestPersistentProcessBDDFeaturesParse(t *testing.T) {
	suite := godog.TestSuite{Options: &godog.Options{Paths: []string{"features"}}}
	features, err := suite.RetrieveFeatures()
	if err != nil {
		t.Fatalf("parse persistent-process features: %v", err)
	}
	if len(features) != 15 {
		t.Fatalf("parsed feature count = %d, want 15", len(features))
	}
}

// TestPersistentProcessBDD executes the real-CLI persistent-process suite.
// It has two deliberate gates: the e2e build tag and AGENT_ADAPTOR_E2E=1.
// The suite may consume locally authenticated provider quota.
func TestPersistentProcessBDD(t *testing.T) {
	if strings.TrimSpace(os.Getenv("AGENT_ADAPTOR_E2E")) != "1" {
		t.Skip("set AGENT_ADAPTOR_E2E=1 in addition to -tags=e2e to run paid real-CLI BDD")
	}
	tags := strings.TrimSpace(os.Getenv("AGENT_ADAPTOR_E2E_TAGS"))
	if tags == "" {
		tags = defaultE2ETags
	}
	paths := []string{"features"}
	if configured := strings.TrimSpace(os.Getenv("AGENT_ADAPTOR_E2E_PATHS")); configured != "" {
		paths = splitNonEmpty(configured)
	}
	format := strings.TrimSpace(os.Getenv("AGENT_ADAPTOR_E2E_FORMAT"))
	if format == "" {
		format = "pretty"
	}

	suite := godog.TestSuite{
		Name:                "agent-adaptor-real-cli",
		ScenarioInitializer: initializeScenario,
		Options: &godog.Options{
			Format: format, Paths: paths, Tags: normalizeTagExpression(tags),
			Strict: true, Concurrency: 1, TestingT: t,
		},
	}
	executedScenarios.Store(0)
	if status := suite.Run(); status != 0 {
		t.Fatalf("Godog real-CLI suite failed with status %d", status)
	}
	if executedScenarios.Load() == 0 {
		t.Fatal("Godog tag/path filter selected zero scenarios")
	}
}

func splitNonEmpty(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == os.PathListSeparator })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return []string{"features"}
	}
	return out
}

func normalizeTagExpression(value string) string {
	return strings.TrimSpace(strings.NewReplacer(
		"(", "", ")", "", " and ", "&&", " AND ", "&&",
		" or ", ",", " OR ", ",", "not ", "~", "NOT ", "~",
	).Replace(value))
}
