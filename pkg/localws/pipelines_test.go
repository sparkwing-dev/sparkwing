package localws

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAggregatedPipelinesReportsUnreadableRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	t.Setenv("SPARKWING_REPOS", path)
	if err := os.WriteFile(path, []byte("repos: ["), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	aggregatedPipelinesHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/pipelines", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusInternalServerError, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "read repository registry") {
		t.Fatalf("body = %q, want registry read error", rr.Body.String())
	}
}
