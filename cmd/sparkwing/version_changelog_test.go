package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestPrintVersionChangelog_KnownRelease(t *testing.T) {
	version := newestChangelogRelease(t)
	var buf bytes.Buffer
	printVersionChangelog(&buf, VersionReport{CLI: InfoVersion{Installed: version, Semver: version}})
	out := buf.String()
	if !strings.Contains(out, "sparkwing "+version) {
		t.Fatalf("output missing the release header for %q:\n%s", version, out)
	}
	if !strings.Contains(out, "--topic "+docs.ChangelogSlug) {
		t.Fatalf("output missing the changelog topic pointer:\n%s", out)
	}
}

func TestPrintVersionChangelog_BehindAddsPointer(t *testing.T) {
	version := newestChangelogRelease(t)
	var buf bytes.Buffer
	printVersionChangelog(&buf, VersionReport{
		CLI:    InfoVersion{Installed: version, Semver: version},
		Behind: true,
	})
	if !strings.Contains(buf.String(), "newer releases exist") {
		t.Fatalf("behind report should point at newer releases:\n%s", buf.String())
	}
}

func TestPrintVersionChangelog_UnknownFallsBack(t *testing.T) {
	var buf bytes.Buffer
	printVersionChangelog(&buf, VersionReport{CLI: InfoVersion{Installed: "(unknown)"}})
	out := buf.String()
	if !strings.Contains(out, "--topic "+docs.ChangelogSlug) {
		t.Fatalf("fallback should still point at the changelog topic:\n%s", out)
	}
}

func newestChangelogRelease(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(docs.Changelog(), "\n") {
		if !strings.HasPrefix(line, "## [v") {
			continue
		}
		rest := strings.TrimPrefix(line, "## [")
		if i := strings.IndexByte(rest, ']'); i >= 0 {
			return rest[:i]
		}
	}
	t.Fatal("no released version heading in changelog")
	return ""
}
