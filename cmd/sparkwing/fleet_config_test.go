package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetSurveyUsesCompleteMachineConfigWithoutWriting(t *testing.T) {
	for _, body := range []string{
		"runner: {allow_repos: []}\n",
		"profiles: {prod: {controller: {url: https://example.test}, state: {type: sqlite}}}\n",
		"profiles: {prod: {controller: {url: 'https://user:hidden@example.test'}}}\n",
		"repos: {}\n---\nrepos: {}\n",
	} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SPARKWING_CONFIG", path)
			calls := 0
			rows, err := surveyFleet(func(string, ...string) (string, error) { calls++; return "", nil })
			if err == nil || rows != nil || calls != 0 || !strings.Contains(err.Error(), "config.yaml") || strings.Contains(err.Error(), "hidden") {
				t.Fatalf("invalid machine config surveyed: rows=%v calls=%d err=%v", rows, calls, err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(after, []byte(body)) {
				t.Fatalf("survey changed machine config: %q err=%v", after, readErr)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != 1 {
				t.Fatalf("survey created files: %v err=%v", entries, readErr)
			}
		})
	}
}

func TestFleetSurveyReadsFinalRepositorySection(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	if err := os.MkdirAll(filepath.Join(repo, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.yaml")
	body := "repos:\n  repos:\n    - path: " + repo + "\nrun: {cache_from: ''}\nrunner: {repos: [], credentials_dir: ''}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
	rows, err := surveyFleet(func(dir string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "--git-common-dir") {
			return filepath.Join(dir, ".git"), nil
		}
		return "", nil
	})
	if err != nil || len(rows) != 1 || rows[0].Repo != repo {
		t.Fatalf("machine repository list lost: %+v err=%v", rows, err)
	}
}
