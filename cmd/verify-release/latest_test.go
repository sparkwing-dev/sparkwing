package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLatestReleaseSelectionCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "releases.json")
	if err := os.WriteFile(path, []byte(`[[{"tag_name":"v0.52.1","draft":false,"prerelease":false}], [{"tag_name":"v0.52.2","draft":false,"prerelease":false}]]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--latest-tag", "v0.52.3", "--releases", path}); err != nil {
		t.Fatal(err)
	}
}

func TestLatestUsesEveryPublishedStableVersion(t *testing.T) {
	for _, tc := range []struct {
		name, tag, body string
		want, fail      bool
	}{
		{"empty", "v1.0.0", `[[]]`, true, false},
		{"numeric order", "v1.10.0", `[[{"tag_name":"v1.9.0"}]]`, true, false},
		{"older completes later", "v1.9.0", `[[{"tag_name":"v1.8.0"}],[{"tag_name":"v1.10.0"}]]`, false, false},
		{"newer completes later", "v1.10.0", `[[{"tag_name":"v1.9.0"}]]`, true, false},
		{"equal", "v1.9.0", `[[{"tag_name":"v1.9.0"}]]`, false, false},
		{"prerelease candidate", "v2.0.0-rc.1", `[[]]`, false, false},
		{"exclude drafts prereleases invalid", "v1.0.0", `[[{"tag_name":"v9.0.0","draft":true},{"tag_name":"v8.0.0","prerelease":true},{"tag_name":"v7.0.0-rc.1"},{"tag_name":"preview"}]]`, true, false},
		{"API error", "v1.0.0", `{"message":"unavailable"}`, false, true},
		{"missing pages", "v1.0.0", `[]`, false, true},
		{"null page", "v1.0.0", `[null]`, false, true},
		{"invalid candidate", "oops", `[[]]`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := latestPublishedRelease(tc.tag, []byte(tc.body))
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("got %v,%v want %v error=%v", got, err, tc.want, tc.fail)
			}
		})
	}
}

func TestLatestRefusesAFailedPublishedReleaseLookup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nprintf '[[]]'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("GITHUB_REPOSITORY", "fixture/release")
	if err := run([]string{"--latest-tag", "v1.0.0"}); err == nil {
		t.Fatal("failed API lookup authorized a publication")
	}
}
