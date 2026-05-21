package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

// TestRunDocsVersions_EmbeddedOnly verifies the no-flag path is
// hermetic: it never touches the network, and the table includes the
// embedded migration versions.
func TestRunDocsVersions_EmbeddedOnly(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsVersions([]string{"-o", "json"}); err != nil {
			t.Fatalf("docs versions: %v", err)
		}
	})
	var rows []versionRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one embedded version")
	}
	for _, r := range rows {
		if r.Source != "embedded" {
			t.Errorf("row %+v: source should be embedded when --web is off", r)
		}
	}
}

func TestRunDocsVersions_WebMergesRemote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/versions.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"latest":"v0.5.0","versions":["v0.5.0","v0.4.0","v0.3.0"]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(docs.BaseURLEnvVar, srv.URL)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	out := captureStdout(t, func() {
		if err := runDocsVersions([]string{"--web", "-o", "json"}); err != nil {
			t.Fatalf("docs versions --web: %v", err)
		}
	})
	var rows []versionRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	gotLatest := false
	gotRemote := false
	for _, r := range rows {
		if r.Version == "v0.5.0" {
			if !r.IsLatest {
				t.Errorf("v0.5.0 should be flagged as latest")
			}
			gotLatest = true
		}
		if r.Source == "remote" {
			gotRemote = true
		}
	}
	if !gotLatest {
		t.Error("expected v0.5.0 row from server")
	}
	if !gotRemote {
		t.Error("expected at least one row sourced from remote")
	}
}

func TestRunDocsVersions_WebFailureNonZeroExit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(docs.BaseURLEnvVar, srv.URL)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	err := runDocsVersions([]string{"--web"})
	if err == nil {
		t.Fatal("expected error when --web discovery fails")
	}
}

func TestRunDocsRead_VersionMismatchSuggestsWeb(t *testing.T) {
	err := runDocsRead([]string{"--topic", "pipelines", "--version", "v0.1.0"})
	if err == nil {
		t.Fatal("expected error for non-embedded version")
	}
	if !strings.Contains(err.Error(), "--web") {
		t.Errorf("error should suggest --web; got %v", err)
	}
}

func TestRunDocsRead_WebFetchesPerVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/versions.json":
			_, _ = w.Write([]byte(`{"latest":"v0.4.0","versions":["v0.4.0","v0.3.0"]}`))
		case "/docs/v0.3.0/pipelines.md":
			_, _ = w.Write([]byte("# Pipelines (v0.3.0)\nbody\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(docs.BaseURLEnvVar, srv.URL)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	out := captureStdout(t, func() {
		if err := runDocsRead([]string{"--topic", "pipelines", "--version", "v0.3.0", "--web"}); err != nil {
			t.Fatalf("docs read --web: %v", err)
		}
	})
	if !strings.Contains(out, "v0.3.0") {
		t.Errorf("expected v0.3.0 content; got %q", out)
	}
}

func TestRunDocsRead_WebLatestUsesUnversionedURL(t *testing.T) {
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/versions.json":
			_, _ = w.Write([]byte(`{"latest":"v0.4.0","versions":["v0.4.0"]}`))
		case "/docs/pipelines.md":
			_, _ = w.Write([]byte("# Pipelines (latest)\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(docs.BaseURLEnvVar, srv.URL)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	out := captureStdout(t, func() {
		if err := runDocsRead([]string{"--topic", "pipelines", "--version", "latest", "--web"}); err != nil {
			t.Fatalf("docs read --version latest --web: %v", err)
		}
	})
	if !strings.Contains(out, "latest") {
		t.Errorf("expected latest body; got %q", out)
	}
	if hits["/docs/pipelines.md"] == 0 {
		t.Errorf("expected /docs/pipelines.md fetch; hits = %v", hits)
	}
}

func TestRunDocsRead_WebUnknownVersionMessagesVersionList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/versions.json" {
			_, _ = w.Write([]byte(`{"latest":"v0.4.0","versions":["v0.4.0"]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(docs.BaseURLEnvVar, srv.URL)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	err := runDocsRead([]string{"--topic", "pipelines", "--version", "v9.9.9", "--web"})
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
	if !strings.Contains(err.Error(), "Available") {
		t.Errorf("error should list available versions; got %v", err)
	}
}

func TestRunDocsMigrationsRead_WebFetches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/versions.json":
			_, _ = w.Write([]byte(`{"latest":"v0.4.0","versions":["v0.4.0","v0.5.0"]}`))
		case "/migrations/v0.5.0.md":
			_, _ = w.Write([]byte("# Migrating to v0.5.0\nbody\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(docs.BaseURLEnvVar, srv.URL)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	out := captureStdout(t, func() {
		if err := runDocsMigrationsRead([]string{"--version", "v0.5.0", "--web"}); err != nil {
			t.Fatalf("migrations read --web: %v", err)
		}
	})
	if !strings.Contains(out, "v0.5.0") {
		t.Errorf("expected v0.5.0 body; got %q", out)
	}
}

func TestRunDocsCache_ClearOnEmptyIsNotAnError(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := runDocsCache([]string{"clear"}); err != nil {
		t.Errorf("clear on empty cache should not error; got %v", err)
	}
}

func TestRunDocsCache_InfoOnEmptyDescribesAbsence(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	out := captureStdout(t, func() {
		if err := runDocsCache([]string{"info"}); err != nil {
			t.Errorf("info on empty cache: %v", err)
		}
	})
	if !strings.Contains(out, "not yet created") {
		t.Errorf("expected absent-cache note; got %q", out)
	}
}
