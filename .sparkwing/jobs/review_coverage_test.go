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
	for _, kind := range []string{"delete", "mode", "rename-in", "rename-out", "rename-between-strict", "copy-in", "copy-out"} {
		t.Run(kind, func(t *testing.T) {
			fx := newReviewFixture(t)
			root := t.TempDir()
			gitInit(t, root)
			source := "internal/store/billing_source \xff.sh"
			destination := "internal/store/billing_destination \xfe.sh"
			if kind == "rename-in" || kind == "copy-in" {
				source = "ordinary.sh"
			}
			if kind == "rename-out" || kind == "copy-out" {
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
				if strings.HasPrefix(kind, "copy-") {
					writeGoFile(t, filepath.Join(root, destination), "#!/bin/sh\nallowed() { return 0; }\n")
				} else if err := os.Rename(filepath.Join(root, source), filepath.Join(root, destination)); err != nil {
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
				if strings.HasPrefix(kind, "rename-") || strings.HasPrefix(kind, "copy-") {
					for _, want := range []string{"new file mode", "+allowed() { return 0; }"} {
						if !strings.Contains(call.prompt, want) {
							t.Errorf("%s pass%d omitted strict destination %q", kind, pass, want)
						}
					}
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

func TestReviewSensitiveBinaryChangesBlockRegardlessOfModelVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, path, body  string
		executable, block bool
	}{
		{"strict", "internal/store/billing_payload.png", "\x00payload", false, true},
		{"executable", "bin/tool.png", "\x00payload", true, true},
		{"script", "scripts/access.sh", "#!/bin/sh\nallowed() { return 0; }\n# " + strings.Repeat("\xff", 256), false, true},
		{"source", "lib/access.go", "package access\n\x00", false, true},
		{"shebang", "scripts/access.png", "#!/bin/sh\n# \x00", false, true},
		{"ksh", "scripts/access.ksh", "allowed() { return 0; }\n# \x00\nallowed\n", false, true},
		{"extensionless", "data/payload", "\x00payload", false, true},
		{"unknown", "data/payload.mystery", "\x00payload", false, true},
		{"asset", "images/icon.png", "\x89PNG\r\n\x1a\n\x00", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx, root := gitEncodingReviewFixture(t, nil, map[string]string{tc.path: tc.body})
			if tc.executable {
				if err := os.Chmod(filepath.Join(root, tc.path), 0o755); err != nil {
					t.Fatal(err)
				}
				gitCommitAll(t, root, "make executable")
			}
			ctx := newTestContext(t)
			if tc.name == "ksh" {
				if _, err := sparkwing.Exec(ctx, "/bin/sh", filepath.Join(root, tc.path)).Capture(); err != nil {
					t.Fatalf("extension-only script did not execute: %v", err)
				}
			}
			in := reviewInputs{branch: "work", brief: "Review opaque changes"}
			for pass := range 3 {
				if pass == 2 {
					writeGoFile(t, filepath.Join(root, "other.md"), "follow-up\n")
					gitCommitAll(t, root, "follow-up")
				}
				rec, file, err := runReview(ctx, fx.deps, testPolicy(t), in)
				if err != nil {
					t.Fatal(err)
				}
				if !tc.block {
					if rec.Result != resultPass {
						t.Fatalf("ordinary asset blocked: %+v", rec)
					}
					continue
				}
				if rec.Result != resultBlock || rec.BlockingOpen != 1 {
					t.Fatalf("pass %d: opaque change accepted after empty model verdict: %+v", pass, rec)
				}
				var finding reviewFinding
				for _, f := range rec.Findings {
					if strings.Contains(f.Summary, "binary or unreadable change requires human review: "+tc.path) {
						finding = f
					}
				}
				if finding.ID == "" || finding.Status != findingOpen {
					t.Fatalf("missing human-review finding: %+v", rec.Findings)
				}
				if pass > 0 && !strings.Contains(strings.Join(rec.Notes, "\n"), "no override") {
					t.Fatalf("skip refusal suggests the model can clear a mechanical block: %v", rec.Notes)
				}
				in.skip = finding.ID + "=inspected by author"
				// safety: even a cached record from an older review must not bypass the current content check.
				rec.Findings = nil
				rec.Result, rec.BlockingOpen = resultPass, 0
				if err := saveReviewRecord(file, rec); err != nil {
					t.Fatal(err)
				}
			}
			if len(fx.def.calls) != 2 {
				t.Fatalf("cached tree reran model: %d calls", len(fx.def.calls))
			}
		})
	}
}

func TestReviewBinaryCopyFromStrictPathStillBlocks(t *testing.T) {
	source, destination := "internal/store/billing_payload.bin", "images/payload.png"
	fx, _ := gitEncodingReviewFixture(t, map[string]string{source: "\x00payload"}, map[string]string{destination: "\x00payload"})
	rec, _, err := runReview(newTestContext(t), fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review strict binary copy"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Result != resultBlock || rec.BlockingOpen != 1 || len(fx.sec.calls) != 1 {
		t.Fatalf("strict binary copied out of scope passed: %+v", rec)
	}
}

func TestReviewStrictColonPathsRemainLiteral(t *testing.T) {
	for _, name := range []string{"text", "binary", "pathspec-magic"} {
		t.Run(name, func(t *testing.T) {
			binary := name != "text"
			path, body := "internal/store/alias:billing_payload.go", "package changed\n"
			if binary {
				path, body = "internal/store/alias:billing_payload.png", "\x00unreviewed"
			}
			policy := testPolicy(t)
			if !binary {
				policy.Strict[0].Paths = []string{"internal/**/*.go"}
			}
			if name == "pathspec-magic" {
				path = ":(glob)billing_payload.png"
				policy.Strict[0].Paths = []string{path}
			}
			fx, root := gitEncodingReviewFixture(t, nil, map[string]string{path: body})
			if !binary {
				reply := fmt.Sprintf(`{"findings":[{"id":"S1","severity":"P2","file":%q,"summary":"strict-path defect"}]}`, path+":4:2")
				fx.sec.replies = []string{reply, reply}
			}
			ctx := newTestContext(t)
			for pass := range 2 {
				if pass == 1 {
					writeGoFile(t, filepath.Join(root, "other.md"), "follow-up\n")
					gitCommitAll(t, root, "follow-up")
				}
				rec, _, err := runReview(ctx, fx.deps, policy, reviewInputs{branch: "work", brief: "Review literal strict path"})
				if err != nil {
					t.Fatal(err)
				}
				if rec.Result != resultBlock || rec.BlockingOpen != 1 || len(fx.sec.calls) != pass+1 {
					t.Fatalf("pass%d: colon-bearing strict file escaped review: %+v; security calls=%d", pass, rec, len(fx.sec.calls))
				}
				call := fx.sec.calls[pass]
				if (call.session != "") != (pass == 1) || !strings.Contains(call.prompt, path) {
					t.Fatalf("pass%d: strict prompt lost literal path/session", pass)
				}
			}
		})
	}
}

func TestReviewFindingLocationsUseChangedPathInventory(t *testing.T) {
	for _, tc := range []struct {
		name, path, strict, location, severity string
		block                                  bool
	}{
		{"numeric filename line", "internal/store/a.go:12", "internal/store/a.go:12", "internal/store/a.go:12:4", "P2", true},
		{"two numeric filename suffixes", "internal/store/a.go:12:34", "internal/store/a.go:12:34", "internal/store/a.go:12:34:4", "P2", true},
		{"line and column", "internal/store/a.go:12", "internal/store/a.go:12", "internal/store/a.go:12:4:2", "P2", true},
		{"exact numeric filename", "internal/store/a.go:12", "internal/store/a.go:12", "internal/store/a.go:12", "P2", true},
		{"unmatched", "ordinary.go", "internal/store/strict.go", "missing.go:4", "P2", true},
		{"unmatched suggestion", "ordinary.go", "internal/store/strict.go", "missing.go:4", "P3", true},
		{"missing", "ordinary.go", "internal/store/strict.go", "", "P2", true},
		{"malformed suffix", "ordinary.go", "internal/store/strict.go", "ordinary.go:not-a-line", "P2", true},
		{"path boundary", "ordinary.go", "internal/store/strict.go", "ordinary.go-extra:4", "P2", true},
		{"known ordinary", "ordinary.go", "internal/store/strict.go", "ordinary.go:4:2", "P2", false},
		{"longest path wins", "internal/store/a.go:12", "internal/store/a.go", "internal/store/a.go:12:4", "P2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changes := map[string]string{tc.path: "package changed\n"}
			if tc.name == "longest path wins" {
				changes[tc.strict] = "package strict\n"
			}
			fx, root := gitEncodingReviewFixture(t, nil, changes)
			policy := testPolicy(t)
			policy.Strict[0].Paths = []string{tc.strict}
			reply := fmt.Sprintf(`{"findings":[{"id":"F1","severity":%q,"file":%q,"summary":"real open defect"}]}`, tc.severity, tc.location)
			fx.def.replies = []string{reply, reply}
			ctx := newTestContext(t)
			in := reviewInputs{branch: "work", brief: "Resolve model locations against actual changed paths"}
			for pass := range 3 {
				if pass == 2 {
					writeGoFile(t, filepath.Join(root, "other.md"), "follow-up\n")
					gitCommitAll(t, root, "follow-up")
				}
				rec, file, err := runReview(ctx, fx.deps, policy, in)
				if err != nil {
					t.Fatal(err)
				}
				if (rec.Result == resultBlock) != tc.block || (rec.BlockingOpen == 1) != tc.block {
					t.Fatalf("pass%d location=%q: result=%+v", pass, tc.location, rec)
				}
				if len(rec.Findings) != 1 || rec.Findings[0].Status != findingOpen {
					t.Fatalf("finding cleared: %+v", rec.Findings)
				}
				if tc.block {
					in.skip = "F1=author disagrees"
					if pass == 0 {
						rec.Findings[0].Status, rec.Findings[0].SkipReason = findingSkipped, "old skip"
						rec.Result, rec.BlockingOpen = resultPass, 0
						if err := saveReviewRecord(file, rec); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if len(fx.def.calls) != 2 || fx.def.calls[0].session != "" || fx.def.calls[1].session == "" {
				t.Fatalf("fresh/cached/resumed modes not exercised: %+v", fx.def.calls)
			}
		})
	}
}
