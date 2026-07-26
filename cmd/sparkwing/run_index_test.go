package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseRunFlags_Index(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--sw-index", "/tmp/snap.index"}, "/tmp/snap.index"},
		{"equals-form", []string{"--sw-index=/tmp/snap.index"}, "/tmp/snap.index"},
		{"empty-trailing-flag-falls-through", []string{"--sw-index"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, pass := parseRunFlags(tc.args)
			if wf.index != tc.want {
				t.Errorf("index = %q, want %q", wf.index, tc.want)
			}
			if tc.want == "" && !slices.Contains(pass, "--sw-index") {
				t.Errorf("incomplete --sw-index should pass through; got passthrough=%v", pass)
			}
		})
	}
}

func TestBindRunIndex_SetsGitIndexFileToAnAbsolutePath(t *testing.T) {
	index := writeIndexFile(t)
	var out bytes.Buffer

	env, err := bindRunIndex([]string{"PATH=/usr/bin"}, index, &out, logFormatJSON)
	if err != nil {
		t.Fatalf("bindRunIndex: %v", err)
	}
	if want := "GIT_INDEX_FILE=" + index; !slices.Contains(env, want) {
		t.Errorf("env missing %q; got %v", want, env)
	}
}

// A relative path is resolved before it is handed on: the pipeline
// binary is exec'd from .sparkwing/, not from the caller's directory.
func TestBindRunIndex_ResolvesARelativePath(t *testing.T) {
	index := writeIndexFile(t)
	t.Chdir(filepath.Dir(index))
	var out bytes.Buffer

	env, err := bindRunIndex(nil, filepath.Base(index), &out, logFormatJSON)
	if err != nil {
		t.Fatalf("bindRunIndex: %v", err)
	}
	got := envValue(env, "GIT_INDEX_FILE")
	if !filepath.IsAbs(got) {
		t.Fatalf("GIT_INDEX_FILE = %q, want an absolute path", got)
	}
	if resolved, err := filepath.EvalSymlinks(got); err != nil || resolved != mustEvalSymlinks(t, index) {
		t.Errorf("GIT_INDEX_FILE = %q, want %q", got, index)
	}
}

func TestBindRunIndex_OverridesAnInheritedGitIndexFile(t *testing.T) {
	index := writeIndexFile(t)
	var out bytes.Buffer

	env, err := bindRunIndex([]string{"GIT_INDEX_FILE=/elsewhere/.git/index"}, index, &out, logFormatJSON)
	if err != nil {
		t.Fatalf("bindRunIndex: %v", err)
	}
	if got := envValue(env, "GIT_INDEX_FILE"); got != index {
		t.Errorf("GIT_INDEX_FILE = %q, want %q", got, index)
	}
	if n := countEnvKey(env, "GIT_INDEX_FILE"); n != 1 {
		t.Errorf("GIT_INDEX_FILE appears %d times, want 1 (getenv returns the first match)", n)
	}
}

func TestBindRunIndex_AnnouncesTheBoundIndex(t *testing.T) {
	index := writeIndexFile(t)
	var out bytes.Buffer

	if _, err := bindRunIndex(nil, index, &out, logFormatJSON); err != nil {
		t.Fatalf("bindRunIndex: %v", err)
	}
	var rec struct {
		Event string         `json:"event"`
		Attrs map[string]any `json:"attrs"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &rec); err != nil {
		t.Fatalf("receipt is not JSON (%v): %q", err, out.String())
	}
	if rec.Event != EventIndexBound {
		t.Errorf("event = %q, want %q", rec.Event, EventIndexBound)
	}
	if rec.Attrs["path"] != index {
		t.Errorf("attrs.path = %v, want %q", rec.Attrs["path"], index)
	}
}

// A person watching a run in a terminal gets the receipt as prose; the
// JSON record is for the caller parsing the stream.
func TestBindRunIndex_AnnouncesInProseWhenTheRunIsNotJSON(t *testing.T) {
	index := writeIndexFile(t)
	var out bytes.Buffer

	if _, err := bindRunIndex(nil, index, &out, logFormatPretty); err != nil {
		t.Fatalf("bindRunIndex: %v", err)
	}
	line := strings.TrimSpace(out.String())
	if strings.HasPrefix(line, "{") {
		t.Errorf("receipt = %q, want prose rather than a raw JSON line", line)
	}
	if !strings.Contains(line, index) {
		t.Errorf("receipt = %q, want the bound index named", line)
	}
}

// git reads a missing index as an empty one, so a typo would otherwise
// look like a tree with nothing in it.
func TestBindRunIndex_RejectsAnIndexThatDoesNotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.index")
	var out bytes.Buffer

	if _, err := bindRunIndex(nil, missing, &out, logFormatJSON); err == nil {
		t.Fatal("bindRunIndex: want an error for a missing index, got nil")
	}
	if out.Len() != 0 {
		t.Errorf("a refused binding must not announce itself; got %q", out.String())
	}
}

func writeIndexFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.index")
	if err := os.WriteFile(path, []byte("DIRC"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	return mustEvalSymlinks(t, path)
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return resolved
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

func countEnvKey(env []string, key string) int {
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			n++
		}
	}
	return n
}
