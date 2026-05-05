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
    tags: [ci]
  - name: deploy
    entrypoint: Deploy
    tags: [ci, prod]
`
	if err := os.WriteFile(filepath.Join(spark, "pipelines.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWD, _ := os.Getwd()
	defer os.Chdir(oldWD)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	pipelinesHandler()(rec, httptest.NewRequest(http.MethodGet, "/api/v1/pipelines", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var body struct {
		Pipelines map[string]struct {
			Args []any    `json:"args"`
			Tags []string `json:"tags"`
		} `json:"pipelines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(body.Pipelines) != 2 {
		t.Fatalf("pipelines=%d want 2", len(body.Pipelines))
	}
	deploy, ok := body.Pipelines["deploy"]
	if !ok {
		t.Fatalf("deploy pipeline missing: %+v", body.Pipelines)
	}
	if len(deploy.Tags) != 2 {
		t.Errorf("deploy tags=%v want [ci prod]", deploy.Tags)
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
	pipelinesHandler()(rec, httptest.NewRequest(http.MethodGet, "/api/v1/pipelines", nil))
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
