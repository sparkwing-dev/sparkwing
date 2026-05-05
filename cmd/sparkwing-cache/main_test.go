package main

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	handleHealthCombined(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestArtifactUpload(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	body := strings.NewReader("test content")
	req := httptest.NewRequest("POST", "/artifacts/job123?path=coverage/report.html", body)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify file was created
	data, err := os.ReadFile(filepath.Join(artifactsDir, "job123", "coverage", "report.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "test content" {
		t.Errorf("expected 'test content', got %s", data)
	}
}

func TestArtifactUpload_MissingPath(t *testing.T) {
	req := httptest.NewRequest("POST", "/artifacts/job123", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 without path, got %d", w.Code)
	}
}

func TestArtifactUpload_DirectoryTraversal(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	req := httptest.NewRequest("POST", "/artifacts/job123?path=../../etc/passwd", strings.NewReader("evil"))
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for traversal, got %d", w.Code)
	}
}

func TestArtifactList(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	// Create some files
	os.MkdirAll(filepath.Join(artifactsDir, "job123", "sub"), 0o755)
	os.WriteFile(filepath.Join(artifactsDir, "job123", "a.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(artifactsDir, "job123", "sub", "b.txt"), nil, 0o644)

	req := httptest.NewRequest("GET", "/artifacts/job123", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	var files []string
	json.NewDecoder(w.Body).Decode(&files)
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
}

func TestArtifactList_Empty(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	req := httptest.NewRequest("GET", "/artifacts/nonexistent", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	var files []string
	json.NewDecoder(w.Body).Decode(&files)
	if len(files) != 0 {
		t.Errorf("expected empty, got %v", files)
	}
}

func TestArtifactDownload_SingleFile(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	os.MkdirAll(filepath.Join(artifactsDir, "job123"), 0o755)
	os.WriteFile(filepath.Join(artifactsDir, "job123", "report.html"), []byte("html content"), 0o644)

	req := httptest.NewRequest("GET", "/artifacts/job123?glob=*.html", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), "html content") {
		t.Errorf("expected html content, got %s", body)
	}
}

func TestArtifactDownload_NotFound(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	os.MkdirAll(filepath.Join(artifactsDir, "job123"), 0o755)

	req := httptest.NewRequest("GET", "/artifacts/job123?glob=*.xyz", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404 for no matches, got %d", w.Code)
	}
}

func TestValidateGitRef(t *testing.T) {
	valid := []string{"main", "feature/foo", "v1.0.0", "release-2.3", "HEAD"}
	for _, ref := range valid {
		if err := validateGitRef(ref); err != nil {
			t.Errorf("expected %q to be valid, got: %v", ref, err)
		}
	}

	invalid := []string{"", "; rm -rf /", "main$(evil)", "branch name", "a..b", "--format=evil"}
	for _, ref := range invalid {
		if err := validateGitRef(ref); err == nil {
			t.Errorf("expected %q to be invalid", ref)
		}
	}
}

func TestArtifactUpload_AbsolutePath(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	req := httptest.NewRequest("POST", "/artifacts/job123?path=/etc/passwd", strings.NewReader("evil"))
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for absolute path, got %d", w.Code)
	}
}

func TestRepoHash_Deterministic(t *testing.T) {
	h1 := repoHash("git@github.com:user/repo.git")
	h2 := repoHash("git@github.com:user/repo.git")
	if h1 != h2 {
		t.Error("same URL should produce same hash")
	}
	if len(h1) != 12 {
		t.Errorf("expected 12 char hash, got %d", len(h1))
	}
}

func TestRepoHash_Different(t *testing.T) {
	h1 := repoHash("git@github.com:user/repo1.git")
	h2 := repoHash("git@github.com:user/repo2.git")
	if h1 == h2 {
		t.Error("different URLs should produce different hashes")
	}
}

func TestContains(t *testing.T) {
	s := []string{"a", "b", "c"}
	if !contains(s, "b") {
		t.Error("should contain b")
	}
	if contains(s, "d") {
		t.Error("should not contain d")
	}
}
