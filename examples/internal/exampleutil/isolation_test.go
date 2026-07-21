package exampleutil

import (
	"os"
	"testing"
)

func TestTemporaryAgentEnvironmentCreatesAndCleansWorkspace(t *testing.T) {
	environment, err := NewTemporaryAgentEnvironment("test")
	if err != nil {
		t.Fatal(err)
	}
	root := environment.RootDir
	if _, err := os.Stat(environment.WorkspaceDir); err != nil {
		t.Fatalf("workspace was not created: %v", err)
	}
	environment.Cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root still exists after cleanup: %v", err)
	}
}
