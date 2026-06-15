package jobs

import (
	"strings"
	"testing"
)

func TestUnreleasedEntries(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			name: "empty section, only blank lines",
			body: "# Changelog\n\n## [Unreleased]\n\n## [v1.0.0]\n\n- old entry\n",
			want: 0,
		},
		{
			name: "empty section, only sub-headings",
			body: "# Changelog\n\n## [Unreleased]\n\n### Added\n\n### Changed\n\n## [v1.0.0]\n",
			want: 0,
		},
		{
			name: "one entry under Added",
			body: "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- new thing\n\n## [v1.0.0]\n",
			want: 1,
		},
		{
			name: "entries across multiple sub-sections",
			body: "## [Unreleased]\n### Added\n- a\n- b\n### Fixed\n- c\n## [v1.0.0]\n- old\n",
			want: 3,
		},
		{
			name: "no [Unreleased] section",
			body: "# Changelog\n\n## [v1.0.0]\n\n- something\n",
			want: 0,
		},
		{
			name: "bare 'Unreleased' (no brackets) also recognized",
			body: "## Unreleased\n\n- entry\n\n## [v1.0.0]\n",
			want: 1,
		},
		{
			name: "entries in old version do not count",
			body: "## [Unreleased]\n\n## [v1.0.0]\n- old\n- older\n",
			want: 0,
		},
		{
			name: "stops at next top-level heading, ignores indented dashes",
			body: "## [Unreleased]\n\n### Added\n  -- this is not a bullet, dashed prose\n- real bullet\n## [v1.0.0]\n",
			want: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := unreleasedEntries(c.body)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %d entries, want %d", got, c.want)
			}
		})
	}
}

func TestVersionEntries(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		version string
		want    int
	}{
		{
			name:    "version section with three entries",
			body:    "## [Unreleased]\n\n## [v1.0.0] - 2026-05-20\n\n### Added\n- a\n- b\n### Fixed\n- c\n",
			version: "v1.0.0",
			want:    3,
		},
		{
			name:    "version section without date",
			body:    "## [Unreleased]\n\n## [v1.0.0]\n\n- a\n- b\n",
			version: "v1.0.0",
			want:    2,
		},
		{
			name:    "absent version",
			body:    "## [Unreleased]\n- entry\n## [v0.9.0]\n- old\n",
			version: "v1.0.0",
			want:    0,
		},
		{
			name:    "stops at next ## heading",
			body:    "## [v1.0.0]\n- a\n## [v0.9.0]\n- old\n- older\n",
			version: "v1.0.0",
			want:    1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := versionEntries(c.body, c.version)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %d entries, want %d", got, c.want)
			}
		})
	}
}

func TestRewriteUnreleasedToVersion(t *testing.T) {
	body := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- thing one\n\n## [v0.9.0]\n\n- prior\n"
	out, err := rewriteUnreleasedToVersion(body, "v1.0.0", "2026-05-20")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "## [Unreleased]") {
		t.Errorf("expected fresh [Unreleased] heading, got:\n%s", out)
	}
	if !strings.Contains(out, "## [v1.0.0] - 2026-05-20") {
		t.Errorf("expected [v1.0.0] - 2026-05-20 heading, got:\n%s", out)
	}
	idxVer := strings.Index(out, "## [v1.0.0]")
	idxThing := strings.Index(out, "- thing one")
	idxOld := strings.Index(out, "## [v0.9.0]")
	if !(idxVer < idxThing && idxThing < idxOld) {
		t.Fatalf("expected `- thing one` between [v1.0.0] and [v0.9.0]; positions ver=%d thing=%d old=%d\n%s",
			idxVer, idxThing, idxOld, out)
	}
}

func TestRewriteUnreleasedToVersion_NoUnreleasedHeading(t *testing.T) {
	body := "# Changelog\n\n## [v1.0.0]\n\n- old\n"
	if _, err := rewriteUnreleasedToVersion(body, "v1.0.1", "2026-05-20"); err == nil {
		t.Fatal("expected error when [Unreleased] absent, got nil")
	}
}

func TestValidateReleaseVersion_Pre1Lock(t *testing.T) {
	cases := []struct {
		version string
		wantErr string
	}{
		{version: "v0.1.0", wantErr: ""},
		{version: "v0.6.1", wantErr: ""},
		{version: "v0.99.999", wantErr: ""},
		{version: "v1.0.0", wantErr: "pre-1.0 lock"},
		{version: "v1.2.3", wantErr: "pre-1.0 lock"},
		{version: "v2.0.0", wantErr: "pre-1.0 lock"},
	}
	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			err := validateReleaseVersion(c.version)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestPlanChangelogRewrite(t *testing.T) {
	const date = "2026-05-20"
	cases := []struct {
		name     string
		body     string
		version  string
		wantKind changelogRewriteKind
		wantErr  string
		wantBody string
	}{
		{
			name:     "apply when Unreleased has content and version absent",
			body:     "## [Unreleased]\n\n### Added\n- thing\n",
			version:  "v1.0.0",
			wantKind: rewriteApply,
			wantBody: "## [v1.0.0] - ",
		},
		{
			name:     "noop when version already populated and Unreleased empty",
			body:     "## [Unreleased]\n\n## [v1.0.0] - 2026-05-20\n- thing\n",
			version:  "v1.0.0",
			wantKind: rewriteNoop,
		},
		{
			name:    "refuse when both Unreleased and version populated",
			body:    "## [Unreleased]\n- new\n## [v1.0.0]\n- already\n",
			version: "v1.0.0",
			wantErr: "BOTH [Unreleased]",
		},
		{
			name:    "refuse when nothing to ship",
			body:    "## [Unreleased]\n\n## [v0.9.0]\n- old\n",
			version: "v1.0.0",
			wantErr: "[Unreleased] is empty",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := planChangelogRewrite(c.body, c.version)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.kind != c.wantKind {
				t.Fatalf("kind = %v, want %v", got.kind, c.wantKind)
			}
			if c.wantBody != "" && !strings.Contains(got.newBody, c.wantBody) {
				t.Fatalf("newBody missing %q:\n%s", c.wantBody, got.newBody)
			}
			_ = date
		})
	}
}
