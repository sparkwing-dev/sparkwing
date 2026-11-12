package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestReviewBinarySummariesRequireBlobInspectionBeforeApproval(t *testing.T) {
	for name, padding := range map[string]string{"invalid": strings.Repeat("\xff", 256), "control": strings.Repeat("\x01", 256), "nul": "\x00"} {
		t.Run(name, func(t *testing.T) {
			path := "internal/store/billing_access.sh"
			before := "#!/bin/sh\nallowed() { return 1; }\nallowed\n"
			after := "#!/bin/sh\nallowed() { return 0; }\n# " + padding + "\nallowed\n"
			fx, root := gitEncodingReviewFixture(t, map[string]string{path: before}, map[string]string{path: after})
			ctx := newTestContext(t)
			if _, err := sparkwing.Exec(ctx, "/bin/sh", filepath.Join(root, path)).Capture(); err != nil {
				t.Fatalf("padded executable did not run: %v", err)
			}
			base := strings.TrimSpace(gitOutput(t, root, "rev-parse", "origin/main:"+path))
			head := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD:"+path))
			for pass := range 2 {
				if pass == 1 {
					writeGoFile(t, filepath.Join(root, "other.md"), "follow-up\n")
					gitCommitAll(t, root, "follow-up")
				}
				if _, _, err := runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review executable access"}); err != nil {
					t.Fatal(err)
				}
				for profile, rv := range map[string]*fakeReviewer{"default": fx.def, "security": fx.sec} {
					if len(rv.calls) <= pass {
						t.Fatalf("%s pass%d skipped", profile, pass)
					}
					call := rv.calls[pass]
					if (call.session != "") != (pass == 1) {
						t.Fatalf("%s pass%d wrong session", profile, pass)
					}
					if !utf8.ValidString(call.prompt) {
						t.Errorf("%s pass%d invalid prompt UTF-8", profile, pass)
					}
					_, summary, found := strings.Cut(call.prompt, fmt.Sprintf("Binary file %q", path))
					if !found || !strings.HasPrefix(strings.TrimSpace(summary), "UNREVIEWED CONTENT") {
						t.Errorf("%s pass%d binary summary lacks its coverage warning", profile, pass)
					}
					for _, want := range []string{"UNREVIEWED CONTENT", "mode=100644", "must inspect", "before approving", fmt.Sprintf("blob=%s", base), fmt.Sprintf("blob=%s", head), "git cat-file blob", "od -An -tx1 -v"} {
						if !strings.Contains(call.prompt, want) {
							t.Errorf("%s pass%d omitted binary inspection contract %q", profile, pass, want)
						}
					}
				}
			}
		})
	}
}

func TestReviewEscapedPatchesDistinguishLiteralBackslashesFromInvalidBytes(t *testing.T) {
	path := "internal/store/billing_access.sh"
	fx, root := gitEncodingReviewFixture(t,
		map[string]string{path: "#!/bin/sh\n# \\xff\n# common \xfe\n"},
		map[string]string{path: "#!/bin/sh\n# \xff\n# common \xfe\n"})
	ctx := newTestContext(t)
	for pass := range 2 {
		if pass == 1 {
			writeGoFile(t, filepath.Join(root, "other.md"), "follow-up\n")
			gitCommitAll(t, root, "follow-up")
		}
		if _, _, err := runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review byte identity"}); err != nil {
			t.Fatal(err)
		}
		for profile, rv := range map[string]*fakeReviewer{"default": fx.def, "security": fx.sec} {
			if len(rv.calls) <= pass {
				t.Fatalf("%s pass%d skipped", profile, pass)
			}
			call := rv.calls[pass]
			if (call.session != "") != (pass == 1) {
				t.Fatalf("%s pass%d wrong session", profile, pass)
			}
			for _, want := range []string{"contains invalid UTF-8; bytes escaped", "\\\\ decodes to one literal backslash", "\\xNN decodes to one byte", "-# \\\\xff\n", "+# \\xff\n"} {
				if !strings.Contains(call.prompt, want) {
					t.Errorf("%s pass%d lost byte identity %q", profile, pass, want)
				}
			}
			if strings.Contains(call.prompt, "-# \\xff\n+# \\xff\n") {
				t.Errorf("%s pass%d distinct bytes have identical patch lines", profile, pass)
			}
		}
	}
}

func TestReviewStrictRoutingIncludesRenameSourcesAndDestinations(t *testing.T) {
	for _, kind := range []string{"delete", "mode", "rename-in", "rename-out", "rename-between-strict"} {
		t.Run(kind, func(t *testing.T) {
			fx := newReviewFixture(t)
			root := t.TempDir()
			gitInit(t, root)
			source := "internal/store/billing_source \xff.sh"
			destination := "internal/store/billing_destination \xfe.sh"
			if kind == "rename-in" {
				source = "ordinary.sh"
			}
			if kind == "rename-out" {
				destination = "ordinary.sh"
			}
			writeGoFile(t, filepath.Join(root, source), "#!/bin/sh\nallowed() { return 0; }\n")
			gitCommitAll(t, root, "base")
			base := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
			runTestGit(t, root, "update-ref", "refs/remotes/origin/main", base)
			switch kind {
			case "delete":
				if err := os.Remove(filepath.Join(root, source)); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(filepath.Join(root, source), 0o755); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.MkdirAll(filepath.Dir(filepath.Join(root, destination)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(root, source), filepath.Join(root, destination)); err != nil {
					t.Fatal(err)
				}
			}
			gitCommitAll(t, root, "move security boundary")
			fx.deps.root = root
			fx.deps.git = func(ctx context.Context, args ...string) (string, error) { return reviewGit(ctx, root, args...) }
			fx.def.replies = []string{`{"findings":[]}`, `{"findings":[]}`}
			fx.sec.replies = []string{`{"findings":[]}`, `{"findings":[]}`}
			ctx := newTestContext(t)
			for pass := range 2 {
				if pass == 1 {
					writeGoFile(t, filepath.Join(root, "other.md"), "follow-up\n")
					gitCommitAll(t, root, "follow-up")
				}
				if _, _, err := runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review strict source and destination"}); err != nil {
					t.Fatal(err)
				}
				if len(fx.sec.calls) <= pass {
					t.Fatalf("security pass%d skipped for %s", pass, kind)
				}
				call := fx.sec.calls[pass]
				if (call.session != "") != (pass == 1) {
					t.Fatalf("pass%d wrong session", pass)
				}
				if !utf8.ValidString(call.prompt) || strings.ContainsRune(call.prompt, utf8.RuneError) {
					t.Errorf("pass%d lost filename byte identity", pass)
				}
				switch kind {
				case "delete", "rename-out", "rename-between-strict":
					for _, want := range []string{"deleted file mode", "-allowed() { return 0; }"} {
						if !strings.Contains(call.prompt, want) {
							t.Errorf("%s pass%d omitted strict source deletion %q", kind, pass, want)
						}
					}
				case "mode":
					if !strings.Contains(call.prompt, "new mode 100755") {
						t.Errorf("pass%d omitted mode change", pass)
					}
				}
				if kind == "rename-in" || kind == "rename-between-strict" {
					for _, want := range []string{"new file mode", "+allowed() { return 0; }"} {
						if !strings.Contains(call.prompt, want) {
							t.Errorf("%s pass%d omitted strict destination %q", kind, pass, want)
						}
					}
				}
				if kind == "rename-out" && strings.Contains(call.prompt, "+++ b/ordinary.sh") {
					t.Errorf("pass%d strict scope included ordinary destination", pass)
				}
			}
		})
	}
}

func TestReviewAttributesCannotHideExecutableTextChanges(t *testing.T) {
	path := "internal/store/billing_access.sh"
	fx, root := gitEncodingReviewFixture(t,
		map[string]string{path: "allowed() { return 1; }\n"},
		map[string]string{path: "allowed() { return 0; }\n", ".gitattributes": "*.sh binary\n"})
	ctx := newTestContext(t)
	for pass := range 2 {
		if pass == 1 {
			writeGoFile(t, filepath.Join(root, "other.md"), "follow-up\n")
			gitCommitAll(t, root, "follow-up")
		}
		if _, _, err := runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review executable text"}); err != nil {
			t.Fatal(err)
		}
		for profile, rv := range map[string]*fakeReviewer{"default": fx.def, "security": fx.sec} {
			if len(rv.calls) <= pass {
				t.Fatalf("%s pass%d skipped", profile, pass)
			}
			for _, want := range []string{"-allowed() { return 1; }", "+allowed() { return 0; }"} {
				if !strings.Contains(rv.calls[pass].prompt, want) {
					t.Errorf("%s pass%d author-controlled attributes hid %q", profile, pass, want)
				}
			}
		}
	}
}
