package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRepoIdentityMatchesHonoursTheHostBoundary(t *testing.T) {
	for _, tc := range []struct {
		key, repo, url string
		want           bool
	}{
		{"", "acme/web", "", true},
		{"github.com/acme/web", "acme/web", "", true},
		{"acme/web", "acme/web", "", true},
		{"github.com/evil/acme/web", "acme/web", "", false},
		{"gitlab.com/group/sub/web", "sub/web", "", false},
		{"gitlab.com/group/sub/web", "sub/web", "https://gitlab.com/group/sub/web.git", true},
		{"github.com/evil/other", "acme/web", "", false},
		{"github.com/acme/web", "", "", false},
		{"github.com/acme/web", "", "git@github.com:acme/web.git", true},
	} {
		if got := store.RepoIdentityMatches(tc.key, tc.repo, tc.url); got != tc.want {
			t.Errorf("RepoIdentityMatches(%q, %q, %q) = %v, want %v", tc.key, tc.repo, tc.url, got, tc.want)
		}
	}
}

func TestRepoIdentityMatchesLetsTheCloneURLDecide(t *testing.T) {
	url := "https://github.com/acme/web.git"
	cases := []struct {
		key  string
		want bool
	}{
		{"github.com/acme/web", true},
		{"evil.example/acme/web", false},
		{"github.com/x/acme/web", false},
		{"acme/web", false},
	}
	for _, c := range cases {
		if got := store.RepoIdentityMatches(c.key, "acme/web", url); got != c.want {
			t.Errorf("store.RepoIdentityMatches(%q, acme/web, %s) = %v, want %v", c.key, url, got, c.want)
		}
	}
	if !store.RepoIdentityMatches("acme/web", "acme/web", "") {
		t.Error("a row with no clone URL should still match its own slug")
	}
	if store.RepoIdentityMatches("github.com/acme/web", "", "") {
		t.Error("a row with no repository should match no scoped key")
	}
}
