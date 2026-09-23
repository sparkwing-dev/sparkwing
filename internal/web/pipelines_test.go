package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPipelinesHandler_DiscoversYAML(t *testing.T) {
	dir := t.TempDir()
	spark := filepath.Join(dir, ".sparkwing")
	if err := os.Mkdir(spark, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `pipelines:
  - name: build
    entrypoint: Build
  - name: deploy
    entrypoint: Deploy
`
	if err := os.WriteFile(filepath.Join(spark, "sparkwing.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWD, _ := os.Getwd()
	defer os.Chdir(oldWD)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	pipelinesHandler(nil)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/pipelines", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var body struct {
		Pipelines map[string]struct {
			Args []any `json:"args"`
		} `json:"pipelines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(body.Pipelines) != 2 {
		t.Fatalf("pipelines=%d want 2", len(body.Pipelines))
	}
	if _, ok := body.Pipelines["deploy"]; !ok {
		t.Fatalf("deploy pipeline missing: %+v", body.Pipelines)
	}
}

func TestPipelinesHandler_NoYAMLReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	defer os.Chdir(oldWD)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	pipelinesHandler(nil)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/pipelines", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var body struct {
		Pipelines map[string]any `json:"pipelines"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Pipelines) != 0 {
		t.Fatalf("expected empty pipelines, got %+v", body.Pipelines)
	}
}

// A signed-up account's pipelines are its team's, which only the controller
// knows, so the dashboard forwards that session's read; the operator's own
// session still reads the pipelines declared in the working directory.
func TestPipelinesHandler_AccountSessionReadsItsTeamsListFromTheController(t *testing.T) {
	account := newIdentityController(t, true)
	account.account = true
	rec := httptest.NewRecorder()
	teamDashboard(t, account.URL).ServeHTTP(rec, signedInRequest(http.MethodGet, "/api/v1/pipelines", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("account session pipelines = %d, want the controller's 204: %s", rec.Code, rec.Body)
	}
	_, _, upstream, authz := account.snapshot()
	if len(upstream) != 1 || upstream[0] != "GET /api/v1/pipelines" || authz[0] != "Session user-session" {
		t.Fatalf("upstream = %v %v, want one GET /api/v1/pipelines under the user's session", upstream, authz)
	}

	operator := newIdentityController(t, true)
	rec = httptest.NewRecorder()
	teamDashboard(t, operator.URL).ServeHTTP(rec, signedInRequest(http.MethodGet, "/api/v1/pipelines", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator session pipelines = %d, want 200 from the working directory: %s", rec.Code, rec.Body)
	}
	if _, _, upstream, _ := operator.snapshot(); len(upstream) != 0 {
		t.Errorf("the operator's pipelines read reached the controller: %v", upstream)
	}
}
