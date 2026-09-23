package sourceurl

import (
	"strings"
	"testing"
)

func TestIdentityReadsEverySpellingOfOneRepositoryTheSame(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/Acme/App.git",
		"https://github.com/acme/app",
		"https://github.com/acme/app/",
		"https://GitHub.com/acme/app.git/",
		"git@github.com:acme/app.git",
		"ssh://git@github.com/acme/app",
	} {
		got, err := Identity(raw)
		if err != nil {
			t.Fatalf("Identity(%q): %v", raw, err)
		}
		if got != "github.com/acme/app" {
			t.Errorf("Identity(%q) = %q, want github.com/acme/app", raw, got)
		}
	}
}

func TestIdentityRefusesQueriesFragmentsAndDotSegments(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/acme/app.git?x=1",
		"https://github.com/acme/app.git#frag",
		"https://github.com/acme/../other/app.git",
		"https://github.com/acme//app.git",
	} {
		if got, err := Identity(raw); err == nil {
			t.Errorf("Identity(%q) = %q, want a refusal", raw, got)
		}
	}
}

func TestTriggerRepositoryRefusesFieldsThatNameDifferentRepositories(t *testing.T) {
	cases := []struct{ repoURL, env, owner, repo string }{
		{"https://github.com/acme/app.git", "evil/payload", "", ""},
		{"https://gitlab.com/acme/app.git", "acme/app", "", ""},
		{"https://github.com/acme/app.git", "", "evil", "payload"},
		{"", "acme/app", "acme", "other"},
		{"", "", "acme", ""},
	}
	for _, c := range cases {
		if got, err := TriggerRepository(c.repoURL, c.env, c.owner, c.repo); err == nil {
			t.Errorf("TriggerRepository(%+v) = %q, want a refusal", c, got)
		}
	}
}

func TestTriggerRepositoryAcceptsAgreeingFields(t *testing.T) {
	got, err := TriggerRepository("git@github.com:Acme/App.git", "acme/app", "ACME", "app")
	if err != nil || got != "github.com/acme/app" {
		t.Fatalf("TriggerRepository = %q, %v; want github.com/acme/app", got, err)
	}
	if got, err := TriggerRepository("", "", "", ""); err != nil || got != "" {
		t.Fatalf("an unnamed repository = %q, %v; want empty", got, err)
	}
}

func TestRepoAllowlistMatchesOneSegmentPerWildcard(t *testing.T) {
	allow, err := ParseRepoAllowlist([]string{"GitHub.com/acme/*", "gitlab.example.com/team/app.git"})
	if err != nil {
		t.Fatal(err)
	}
	for identity, want := range map[string]bool{
		"github.com/acme/app":            true,
		"github.com/ACME/app":            true,
		"github.com/acme/app/sub":        false,
		"github.com/acmeevil/app":        false,
		"github.com/other/app":           false,
		"evil.com/github.com/acme/app":   false,
		"gitlab.example.com/team/app":    true,
		"gitlab.example.com/team/app2":   false,
		"gitlab.example.com:22/team/app": false,
	} {
		if got := allow.Admits(identity); got != want {
			t.Errorf("Admits(%q) = %v, want %v", identity, got, want)
		}
	}
	if (RepoAllowlist{}).Admits("github.com/acme/app") {
		t.Fatal("the empty allowlist admitted a repository")
	}
}

func TestParseRepoPatternRefusesPatternsThatAreNotAHostAndPath(t *testing.T) {
	for _, raw := range []string{
		"", "*", "https://github.com/acme/*", "git@github.com:acme/*", "github.com",
		"github.com/", "*.com/acme/app", "*/acme/app", "github/acme", "github.com/acme/[a-z",
		"github.com/acme/app;rm", "github.com/acme/app name", "github.com/../app", "github.com//app",
		"'github.com/acme/*'",
	} {
		if got, err := ParseRepoPattern(raw); err == nil {
			t.Errorf("ParseRepoPattern(%q) = %q, want a refusal", raw, got)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Errorf("ParseRepoPattern(%q) gave an empty reason", raw)
		}
	}
}
