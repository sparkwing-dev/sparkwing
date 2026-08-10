package bincache

import (
	"path/filepath"
	"testing"
)

// newWorkspaceAt builds a workspace whose go.work body the caller
// supplies verbatim, so a test can vary one directive at a time.
func newWorkspaceAt(t *testing.T, body string) (pipelineDir string) {
	t.Helper()
	root := t.TempDir()
	pipelineDir = filepath.Join(root, "svc")
	writeFile(t, filepath.Join(pipelineDir, "go.mod"), "module example.com/pipeline\n\ngo 1.22\n")
	writeFile(t, filepath.Join(pipelineDir, "main.go"), "package main\n\nfunc main() {}\n")

	writeFile(t, filepath.Join(root, "tmpl", "go.mod"), "module example.com/tmpl\n\ngo 1.22\n")
	writeFile(t, filepath.Join(root, "tmpl", "registry.go"), "package tmpl\n")

	work := filepath.Join(root, "go.work")
	writeFile(t, work, body)
	t.Setenv("GOWORK", work)
	return pipelineDir
}

// The workspace summary is normalized rather than a hash of the file's
// bytes, which means every build-affecting directive has to be
// enumerated by hand. modfile.WorkFile carries five; one omitted here
// is one that silently stops invalidating the cache.
func TestPipelineCacheKey_InvalidatesOnEachGoWorkDirective(t *testing.T) {
	const base = "go 1.26\n\nuse ./svc\nuse ./tmpl\n"

	for _, tc := range []struct {
		field string
		body  string
	}{
		{"Go", "go 1.25\n\nuse ./svc\nuse ./tmpl\n"},
		{"Toolchain", base + "\ntoolchain go1.26.1\n"},
		{"Godebug", base + "\ngodebug default=go1.25\n"},
		{"Use", "go 1.26\n\nuse ./svc\n"},
		{"Replace", base + "\nreplace example.com/other => example.com/other v1.4.0\n"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			before := mustKey(t, newWorkspaceAt(t, base))
			after := mustKey(t, newWorkspaceAt(t, tc.body))
			if before == after {
				t.Fatalf("changing go.work %s must move the key; got %s twice", tc.field, before)
			}
		})
	}
}

// Cosmetic differences are exactly what raw-byte hashing used to catch
// and what normalization is meant to stop catching: comments, blank
// lines, and directive order differ freely between checkouts.
func TestPipelineCacheKey_IgnoresGoWorkCosmetics(t *testing.T) {
	plain := "go 1.26\n\nuse ./svc\nuse ./tmpl\n"
	dressed := "// local workspace\n\ngo 1.26\n\n\nuse ./tmpl\nuse ./svc // pipeline\n"

	before := mustKey(t, newWorkspaceAt(t, plain))
	after := mustKey(t, newWorkspaceAt(t, dressed))
	if before != after {
		t.Fatalf("comments and ordering must not move the key: %s vs %s", before, after)
	}
}
