package a2a_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These helpers describe only the A2A JSON wire consumed by server tests.
func postRPC(t *testing.T, handler http.Handler, body string) *http.Response {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "https://example.com/a2a", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Result()
}

type taskArtifact struct {
	Name  string             `json:"name"`
	Parts []taskArtifactPart `json:"parts"`
}

type taskArtifactPart struct {
	Text string         `json:"text"`
	Data map[string]any `json:"data"`
}

func findTaskArtifact(t *testing.T, artifacts []taskArtifact, name string) taskArtifact {
	t.Helper()
	for _, artifact := range artifacts {
		if artifact.Name == name {
			return artifact
		}
	}
	t.Fatalf("artifact %q not found in %+v", name, artifacts)
	return taskArtifact{}
}
