package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishedProfileExamplesUseMachineSchema(t *testing.T) {
	if !checkProfileConfigs(filepath.Join("..", "..", "docs")) {
		t.Fatal("published profile examples disagree with the machine decoder")
	}
}

func TestProfileExamplesUseMachineSchema(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"connection", "profiles:\n  prod: {controller: {url: https://api.example, token: hidden}}\nrun: {cache_from: '', submit_env_allow: []}\n", true},
		{"inline", "profiles: {prod: {controller: {url: https://api.example}}}\n", true},
		{"removed_backend", "profiles:\n  prod: {controller: {url: https://api.example}, state: {type: sqlite}}\n", false},
		{"removed_inline_backend", "profiles: {prod: {state: {type: sqlite}}}\n", false},
		{"other_section", "profiles:\n  prod: {controller: {url: https://api.example}}\nrunner: {allow_repos: []}\n", false},
		{"null", "profiles:\n  prod: {controller: {url: https://api.example, token: null}}\n", false},
		{"trailing_document", "profiles:\n  prod: {controller: {url: https://api.example}}\n---\nprofiles: {}\n", false},
		{"credential_url", "profiles:\n  prod: {controller: {url: https://user:hidden@api.example}}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "example.md"), []byte("```yaml\n"+tc.body+"```\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := checkProfileConfigs(dir); got != tc.valid {
				t.Fatalf("valid = %v, want %v", got, tc.valid)
			}
		})
	}
}
