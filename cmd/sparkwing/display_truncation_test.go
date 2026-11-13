package main

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestCompletionHintRuneBoundary(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	projectAt(t, "pipelines:\n  - name: demo\n    entrypoint: Demo\n")
	dir, err := findSparkwingDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, value, want string }{
		{"accent straddles", strings.Repeat("a", 78) + "é-tail", strings.Repeat("a", 77) + "..."},
		{"emoji straddles", strings.Repeat("a", 78) + "😀-tail", strings.Repeat("a", 77) + "..."},
		{"accent fits", strings.Repeat("a", 75) + "é-tail", strings.Repeat("a", 75) + "é..."},
		{"controls", "hello \x1b]52;c;clipboard\a\x1b[2J", `"hello \x1b]52;c;clipboard\a\x1b[2J"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := storeDescribe(dir, "fixture", runspec.DescribePresentation{Pipelines: []runspec.PipelinePresentation{{Name: "Demo", Short: tc.value, Args: []nodewire.ArgumentDescription{}}}}); err != nil {
				t.Fatal(err)
			}
			if got := readDescribeFile(byRepoDescribePath(dir)); len(got) != 1 || got[0].Short != tc.value {
				t.Fatalf("describe fixture unavailable: %+v", got)
			}
			var out strings.Builder
			if err := runInternalCompletePipelines(&out, nil); err != nil {
				t.Fatal(err)
			}
			if got, want := out.String(), "demo\t"+tc.want+"\n"; got != want || !utf8.ValidString(got) {
				t.Fatalf("completion=%q want=%q", got, want)
			}
		})
	}
}

func TestDocsListRuneBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, title, summary string }{
		{"straddle", strings.Repeat("t", 38) + "é-tail", strings.Repeat("s", 68) + "😀-tail"},
		{"fit", strings.Repeat("t", 35) + "é-tail", strings.Repeat("s", 63) + "😀-tail"},
		{"controls", "title\x1b[2J", "summary\x1b]52;c;clipboard\a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := docs.Entry{Slug: "fixture", Title: tc.title, Summary: tc.summary}
			t.Run("pretty", func(t *testing.T) {
				out := captureStdout(t, func() {
					if err := renderDocsList([]docs.Entry{entry}, "pretty"); err != nil {
						t.Fatal(err)
					}
				})
				if !utf8.ValidString(out) || strings.ContainsAny(out, "\x1b\a") {
					t.Fatalf("unsafe docs=%q", out)
				}
				if tc.name == "straddle" && (!strings.Contains(out, strings.Repeat("t", 37)+"...") || !strings.Contains(out, strings.Repeat("s", 67)+"...")) {
					t.Fatalf("docs=%q", out)
				}
				if tc.name == "fit" && (!strings.Contains(out, strings.Repeat("t", 35)+"é...") || !strings.Contains(out, strings.Repeat("s", 63)+"😀...")) {
					t.Fatalf("docs=%q", out)
				}
			})
			t.Run("json", func(t *testing.T) {
				out := captureStdout(t, func() {
					if err := renderDocsList([]docs.Entry{entry}, "json"); err != nil {
						t.Fatal(err)
					}
				})
				var got docs.Entry
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatal(err)
				}
				if got != entry {
					t.Fatalf("JSON=%+v want=%+v", got, entry)
				}
			})
		})
	}
}

func TestMigrationListRuneBoundary(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{
		{"accent straddles", strings.Repeat("s", 68) + "é-tail", strings.Repeat("s", 67) + "..."},
		{"emoji straddles", strings.Repeat("s", 68) + "😀-tail", strings.Repeat("s", 67) + "..."},
		{"accent fits", strings.Repeat("s", 65) + "é-tail", strings.Repeat("s", 65) + "é..."},
		{"controls", "summary\x1b[2J", `"summary\x1b[2J"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := docs.MigrationEntry{Version: "v1.0.0", Summary: tc.value}
			t.Run("pretty", func(t *testing.T) {
				out := captureStdout(t, func() {
					if err := renderMigrationsList([]docs.MigrationEntry{entry}, "pretty"); err != nil {
						t.Fatal(err)
					}
				})
				if !utf8.ValidString(out) || !strings.Contains(out, tc.want) {
					t.Fatalf("migration=%q want summary=%q", out, tc.want)
				}
			})
			t.Run("json", func(t *testing.T) {
				out := captureStdout(t, func() {
					if err := renderMigrationsList([]docs.MigrationEntry{entry}, "json"); err != nil {
						t.Fatal(err)
					}
				})
				var got docs.MigrationEntry
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatal(err)
				}
				if got != entry {
					t.Fatalf("JSON=%+v want=%+v", got, entry)
				}
			})
		})
	}
}
