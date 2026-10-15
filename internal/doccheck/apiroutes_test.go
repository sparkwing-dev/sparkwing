package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentedAPIRoutesResolveAgainstSharedPolicy(t *testing.T) {
	for _, test := range []struct {
		doc string
		bad int
	}{
		{"Use `GET /api/v1/runs/{id}/jobs/{jobID}`.", 0},
		{"Use `GET /api/v1/runs/{id}?details=true`.", 0},
		{"Use `POST /api/v1/runners/claim`.", 0},
		{"`GET /api/v1/data/objects/{key...}`", 0},
		{"`POST /node/v1/masks`", 0},
		{"`POST /api/v1/nodes/claim`", 1},
		{"`GET /api/v1/triggers`", 1},
		{"`DELETE /api/v1/runners/claim`", 1},
		{"`GET /api/v1/runs/{id}/jobs/{jobID}/unknown`", 1},
		{"`GET /api/v1/runs/{id}/nodes/{nodeID}/output`", 1},
		{"`GET\n/api/v1/runs/{id}/nodes/{nodeID}/output`", 1},
		{"GET https://example.com/api/v1/anything\nPOST /third-party/hooks", 0},
	} {
		t.Run(test.doc, func(t *testing.T) {
			refs := documentedAPIRoutes(test.doc)
			var bad int
			for _, ref := range refs {
				if !knownAPIRoute(ref.method, ref.path) {
					bad++
				}
			}
			if bad != test.bad {
				t.Fatalf("unresolved=%d, want %d: %+v", bad, test.bad, refs)
			}
		})
	}
}

func TestAPIRouteCheckReadsNestedDocsAndPreservesMigrationHistory(t *testing.T) {
	dir := t.TempDir()
	for name, doc := range map[string]string{
		"guide.md":            "`POST /api/v1/runners/claim`",
		"nested/guide.md":     "`GET /api/v1/runs/{run}`",
		"migrations/old.md":   "`POST /api/v1/triggers`",
		"nested/not-doc.json": "`POST /api/v1/triggers`",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !checkAPIRoutes(dir) {
		t.Fatal("valid routes or historical migration failed")
	}
	if err := os.WriteFile(filepath.Join(dir, "nested/guide.md"), []byte("`POST /api/v1/triggers`"), 0o600); err != nil {
		t.Fatal(err)
	}
	if checkAPIRoutes(dir) || checkAPIRoutes(filepath.Join(dir, "missing")) {
		t.Fatal("invalid or unreadable documentation passed")
	}
}

func TestPublishedDocumentationUsesFinalAPIRoutes(t *testing.T) {
	if !checkAPIRoutes("../../docs") {
		t.Fatal("published guide documents routes outside the final controller policy")
	}
}

func TestDocumentedAPIRouteLocations(t *testing.T) {
	refs := documentedAPIRoutes("# Guide\n\nUse `GET\n/api/v1/runs/{id}` and `POST /api/v1/runners/claim`.")
	if len(refs) != 2 || refs[0].line != 3 || refs[1].line != 4 || !strings.HasSuffix(refs[0].path, "/{id}") {
		t.Fatalf("lost route locations: %+v", refs)
	}
}
