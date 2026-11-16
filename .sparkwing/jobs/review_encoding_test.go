package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func gitEncodingReviewFixture(t *testing.T, before, after map[string]string) (*reviewFixture, string) {
	t.Helper()
	fx := newReviewFixture(t)
	root := t.TempDir()
	gitInit(t, root)
	writeGoFile(t, filepath.Join(root, ".gitattributes"), "* -text\n")
	for path, body := range before {
		writeGoFile(t, filepath.Join(root, path), body)
	}
	gitCommitAll(t, root, "base")
	base := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", base)
	for path, body := range after {
		writeGoFile(t, filepath.Join(root, path), body)
	}
	gitCommitAll(t, root, "security change")
	fx.deps.root = root
	fx.deps.git = func(ctx context.Context, args ...string) (string, error) { return reviewGit(ctx, root, args...) }
	fx.def.replies = []string{`{"findings":[]}`, `{"findings":[]}`}
	fx.sec.replies = []string{`{"findings":[]}`, `{"findings":[]}`}
	return fx, root
}

func TestReviewEscapedTextReachesBothProfilesAndSessionModes(t *testing.T) {
	shell := "internal/store/billing_access.sh"
	legacy := "internal/store/billing_legacy.sh"
	fx, root := gitEncodingReviewFixture(t,
		map[string]string{shell: "#!/bin/sh\nallowed() { return 1; }\n", legacy: "# legacy\xfe\nallowed() { return 1; }\n"},
		map[string]string{shell: "#!/bin/sh\nallowed() { return 0; }\n# \xff\n# café \ufffd\r\n", legacy: "# clean\nallowed() { return 0; }\n"})
	ctx := newTestContext(t)
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			writeGoFile(t, filepath.Join(root, "other.md"), "follow-up\n")
			gitCommitAll(t, root, "follow-up")
		}
		if _, _, err := runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review shell access"}); err != nil {
			t.Fatal(err)
		}
		for name, reviewer := range map[string]*fakeReviewer{"default": fx.def, "security": fx.sec} {
			if len(reviewer.calls) <= pass {
				t.Fatalf("%s pass%d was skipped", name, pass)
			}
			call := reviewer.calls[pass]
			if (call.session != "") != (pass == 1) {
				t.Fatalf("%s pass%d has wrong session %q", name, pass, call.session)
			}
			if !utf8.ValidString(call.prompt) {
				t.Errorf("%s pass%d has invalid UTF-8", name, pass)
			}
			for _, want := range []string{shell, legacy, "contains invalid UTF-8; bytes escaped", "-allowed() { return 1; }", "+allowed() { return 0; }", `+# \xff`, `-# legacy\xfe`, "+# café \ufffd\r\n"} {
				if !strings.Contains(call.prompt, want) {
					t.Errorf("%s pass%d missing %q", name, pass, want)
				}
			}
			if strings.Contains(call.prompt, "Binary file") {
				t.Errorf("%s pass%d hid executable text as binary", name, pass)
			}
		}
	}
}

func TestReviewStrictRoutingPreservesRawLiteralFilenames(t *testing.T) {
	for name, path := range map[string]string{
		"invalid_utf8": "internal/store/billing_\xff.go",
		"space":        "internal/store/billing_space name.go",
		"newline":      "internal/store/billing_line\nbreak.go",
	} {
		t.Run(name, func(t *testing.T) {
			fx, root := gitEncodingReviewFixture(t, map[string]string{path: "package store\n"}, map[string]string{path: "package store\n// security change\n"})
			ctx := newTestContext(t)
			for pass := 0; pass < 2; pass++ {
				if pass == 1 {
					writeGoFile(t, filepath.Join(root, "other.md"), "follow-up\n")
					gitCommitAll(t, root, "follow-up")
				}
				if _, _, err := runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review strict names"}); err != nil {
					t.Fatal(err)
				}
				if len(fx.sec.calls) <= pass {
					t.Fatalf("security reviewer was skipped in pass%d", pass)
				}
				prompt := fx.sec.calls[pass].prompt
				if !strings.Contains(prompt, "+// security change") {
					t.Errorf("strict patch omitted in pass%d", pass)
				}
				if !utf8.ValidString(prompt) || strings.ContainsRune(prompt, utf8.RuneError) {
					t.Errorf("filename byte identity lost in pass%d", pass)
				}
			}
		})
	}
}

func TestReviewStrictPathspecDoesNotExpandIntoOtherFiles(t *testing.T) {
	strict := "internal/store/billing_*.go"
	outside := "internal/store/billing_other.go"
	fx, _ := gitEncodingReviewFixture(t,
		map[string]string{strict: "package store\n", outside: "package store\n"},
		map[string]string{strict: "package store\n// strict change\n", outside: "package store\n// outside strict review\n"})
	policy := testPolicy(t)
	policy.Strict[0].Paths = []string{"internal/store/billing_[*].go"}
	if _, _, err := runReview(newTestContext(t), fx.deps, policy, reviewInputs{brief: "Review literal paths"}); err != nil {
		t.Fatal(err)
	}
	if len(fx.sec.calls) != 1 {
		t.Fatalf("security reviewer calls=%d", len(fx.sec.calls))
	}
	prompt := fx.sec.calls[0].prompt
	if !strings.Contains(prompt, "+// strict change") || strings.Contains(prompt, "+// outside strict review") {
		t.Fatalf("literal strict path expanded: %s", prompt)
	}
}
