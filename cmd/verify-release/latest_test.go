package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func init() {
	if os.Getenv("SPARKWING_TEST_GH_HELPER") != "1" {
		return
	}
	args := os.Args[1:]
	if len(args) != 4 || args[0] != "api" || args[1] != "--paginate" || args[2] != "--slurp" || args[3] != "repos/"+os.Getenv("GITHUB_REPOSITORY")+"/releases?per_page=100" {
		os.Exit(64)
	}
	_, _ = os.Stdout.WriteString(os.Getenv("SPARKWING_TEST_GH_RESPONSE"))
	if os.Getenv("SPARKWING_TEST_GH_FAIL") == "1" {
		os.Exit(1)
	}
	os.Exit(0)
}

func latestGHFixture(t *testing.T, dir, script, response string, fail bool) {
	t.Helper()
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gh.exe"), body, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_TEST_GH_HELPER", "1")
	t.Setenv("SPARKWING_TEST_GH_RESPONSE", response)
	if fail {
		t.Setenv("SPARKWING_TEST_GH_FAIL", "1")
	} else {
		t.Setenv("SPARKWING_TEST_GH_FAIL", "0")
	}
}

func TestLatestReleaseSelectionCommand(t *testing.T) {
	dir := t.TempDir()
	stub := `#!/bin/sh
test "$1" = api && test "$2" = --paginate && test "$3" = --slurp && test "$4" = 'repos/fixture/release/releases?per_page=100' || exit 1
printf '%s' '[[{"tag_name":"v0.52.1","draft":false,"prerelease":false}],[{"tag_name":"v0.52.2","draft":false,"prerelease":false}]]'
`
	latestGHFixture(t, dir, stub, `[[{"tag_name":"v0.52.1","draft":false,"prerelease":false}],[{"tag_name":"v0.52.2","draft":false,"prerelease":false}]]`, false)
	t.Setenv("PATH", dir)
	t.Setenv("GITHUB_REPOSITORY", "fixture/release")
	if err := run([]string{"--latest-tag", "v0.52.3"}); err != nil {
		t.Fatal(err)
	}
}

func TestLatestLookupKeepsRepositoryInOneEndpointArgument(t *testing.T) {
	dir := t.TempDir()
	stub := `#!/bin/sh
test "$#" -eq 4 && test "$1" = api && test "$2" = --paginate && test "$3" = --slurp && test "$4" = "repos/$GITHUB_REPOSITORY/releases?per_page=100" || exit 1
printf '%s' '[[]]'
`
	latestGHFixture(t, dir, stub, `[[]]`, false)
	t.Setenv("PATH", dir)
	t.Setenv("GITHUB_REPOSITORY", "--hostname=example.invalid/$(exit 99); exit 99")
	if err := run([]string{"--latest-tag", "v1.0.0"}); err != nil {
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
	latestGHFixture(t, dir, "#!/bin/sh\nprintf '[[]]'\nexit 1\n", `[[]]`, true)
	t.Setenv("PATH", dir)
	t.Setenv("GITHUB_REPOSITORY", "fixture/release")
	if err := run([]string{"--latest-tag", "v1.0.0"}); err == nil {
		t.Fatal("failed API lookup authorized a publication")
	}
}
