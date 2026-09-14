package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runTagOrder(t *testing.T, candidate string, existing []string) (string, bool) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	script := filepath.Join(filepath.Dir(thisFile), "check-release-tag-order.sh")
	cmd := exec.Command("bash", script, candidate)
	cmd.Stdin = strings.NewReader(strings.Join(existing, "\n"))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err == nil
}

func TestCheckReleaseTagOrder(t *testing.T) {
	t.Parallel()

	published := []string{"v0.49.9", "v0.50.1", "v0.50.2", "v0.50.3"}

	cases := []struct {
		name       string
		candidate  string
		existing   []string
		wantAccept bool
	}{
		{name: "next patch", candidate: "v0.50.4", existing: published, wantAccept: true},
		{name: "next minor", candidate: "v0.51.0", existing: published, wantAccept: true},
		{name: "equal to newest", candidate: "v0.50.3", existing: published},
		{name: "below newest", candidate: "v0.50.2", existing: published},
		{name: "below newest by minor", candidate: "v0.49.10", existing: published},
		{name: "first tag", candidate: "v0.1.0", wantAccept: true},
		{name: "not semver", candidate: "v0.50", existing: published},
		{name: "no v prefix", candidate: "0.50.4", existing: published},
		{name: "prerelease ahead of newest", candidate: "v0.50.4-rc.1", existing: published, wantAccept: true},
		{name: "prerelease below its own release", candidate: "v0.50.3-rc.1", existing: published},
		{name: "release above its own prerelease", candidate: "v0.50.4", existing: append(published, "v0.50.4-rc.1"), wantAccept: true},
		{name: "second prerelease", candidate: "v0.50.4-rc.2", existing: append(published, "v0.50.4-rc.1"), wantAccept: true},
		{name: "repeated prerelease", candidate: "v0.50.4-rc.1", existing: append(published, "v0.50.4-rc.1")},
		{name: "prerelease behind a published release", candidate: "v0.50.4-rc.1", existing: append(published, "v0.50.4")},
		{name: "ls-remote refs", candidate: "v0.50.4", existing: []string{
			"aa11\trefs/tags/v0.50.3",
			"bb22\trefs/tags/v0.50.3^{}",
			"cc33\trefs/heads/main",
		}, wantAccept: true},
		{name: "ls-remote refs below newest", candidate: "v0.50.3", existing: []string{
			"aa11\trefs/tags/v0.50.3",
			"bb22\trefs/tags/v0.50.3^{}",
		}},
		{name: "ignores non-release tags", candidate: "v0.2.0", existing: []string{"nightly", "v0.1.0", "release-2026"}, wantAccept: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, accepted := runTagOrder(t, c.candidate, c.existing)
			if accepted != c.wantAccept {
				t.Fatalf("accepted = %v, want %v; output:\n%s", accepted, c.wantAccept, out)
			}
			if !accepted && !strings.Contains(out, c.candidate) {
				t.Errorf("refusal does not name the candidate %s:\n%s", c.candidate, out)
			}
		})
	}
}

func TestCheckReleaseTagOrderRequiresACandidate(t *testing.T) {
	t.Parallel()
	if out, accepted := runTagOrder(t, "", nil); accepted {
		t.Fatalf("a missing candidate was accepted:\n%s", out)
	}
}
