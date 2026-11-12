package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReviewPromptsSummarizeBinaryBlobsWithoutChangingText(t *testing.T) {
	fx := newReviewFixture(t)
	root := t.TempDir()
	gitInit(t, root)
	path := "internal/store/billing_policy.sig"
	before := []byte{0xff, 'o', 'l', 'd', '\n', 0}
	after := []byte{0xfe, 'n', 'e', 'w', '\n', 0}
	writeGoFile(t, filepath.Join(root, path), string(before))
	writeGoFile(t, filepath.Join(root, "text.md"), "old café\n")
	hidden := strings.Repeat("unchanged\n", 80) + string(before)
	writeGoFile(t, filepath.Join(root, "internal/store/billing_hidden.bin"), hidden)
	writeGoFile(t, filepath.Join(root, "deleted.bin"), string(before))
	writeGoFile(t, filepath.Join(root, "internal/store/billing_nul.bin"), "\x00old")
	writeGoFile(t, filepath.Join(root, "internal/store/billing_opaque.bin"), strings.Repeat("\xff", 30))
	gitCommitAll(t, root, "base")
	base := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", base)
	if err := os.WriteFile(filepath.Join(root, path), after, 0o644); err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, filepath.Join(root, "text.md"), "new Ελληνικά café \ufffd\r\n")
	writeGoFile(t, filepath.Join(root, "internal/store/billing_hidden.bin"), "changed\n"+hidden)
	writeGoFile(t, filepath.Join(root, "added.bin"), string(after))
	if err := os.Remove(filepath.Join(root, "deleted.bin")); err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, filepath.Join(root, "internal/store/billing_nul.bin"), "\x00new")
	writeGoFile(t, filepath.Join(root, "internal/store/billing_opaque.bin"), strings.Repeat("\xfe", 30))
	gitCommitAll(t, root, "head")
	head := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	textDiff := gitOutput(t, root, "diff", "--no-color", "--no-ext-diff", base, head, "--", "text.md")
	fx.deps.root = root
	fx.deps.git = func(ctx context.Context, args ...string) (string, error) {
		return reviewGit(ctx, root, args...)
	}
	ctx := newTestContext(t)
	fx.def.replies = []string{`{"findings":[]}`, `{"findings":[]}`}
	fx.sec.replies = []string{`{"findings":[]}`, `{"findings":[]}`}
	p := testPolicy(t)
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			writeGoFile(t, filepath.Join(root, "other.md"), "another change\n")
			gitCommitAll(t, root, "follow-up")
		}
		if _, _, err := runReview(ctx, fx.deps, p, reviewInputs{branch: "work", brief: "Review binary metadata and exact text"}); err != nil {
			t.Fatal(err)
		}
		for name, rv := range map[string]*fakeReviewer{"default": fx.def, "security": fx.sec} {
			prompt := rv.calls[pass].prompt
			if !utf8.ValidString(prompt) {
				t.Errorf("%s pass%d received non-UTF-8 stdin", name, pass)
			}
			for _, value := range []string{path, "binary", fmt.Sprintf("sha256=%x", sha256.Sum256(before)), fmt.Sprintf("sha256=%x", sha256.Sum256(after)), "size=6"} {
				if !strings.Contains(strings.ToLower(prompt), value) {
					t.Errorf("%s missing binary metadata %q", name, value)
				}
			}
			if strings.Contains(prompt, string(before)) || strings.Contains(prompt, string(after)) {
				t.Errorf("%s contains binary bytes", name)
			}
			if !strings.Contains(prompt, `Binary file "internal/store/billing_hidden.bin"`) {
				t.Error("non-UTF-8 bytes outside a hunk were not classified as binary")
			}
			for _, path := range []string{"internal/store/billing_nul.bin", "internal/store/billing_opaque.bin"} {
				if !strings.Contains(prompt, fmt.Sprintf("Binary file %q", path)) {
					t.Errorf("missing binary metadata for %q", path)
				}
			}
			if name == "default" {
				for _, path := range []string{"added.bin", "deleted.bin"} {
					if !strings.Contains(prompt, fmt.Sprintf("Binary file %q", path)) {
						t.Errorf("missing binary addition/deletion %s", path)
					}
				}
			}
			if name == "default" && !strings.Contains(prompt, textDiff) {
				t.Error("text diff changed bytes")
			}
		}
	}
}

func TestReviewPromptTruncationPreservesUTF8TextPrefix(t *testing.T) {
	fx := newReviewFixture(t)
	old := fx.deps.git
	header := "diff --git a/cmd/x.go b/cmd/x.go\n@@ -0,0 +1 @@\n+"
	prefix := header + strings.Repeat("a", maxPromptDiff-len(header)-1)
	fx.deps.git = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "diff" && args[1] == "--no-color" {
			return prefix + "€tail", nil
		}
		return old(ctx, args...)
	}
	fx.def.replies = []string{`{"findings":[]}`}
	if _, _, err := runReview(t.Context(), fx.deps, testPolicy(t), reviewInputs{brief: "UTF-8 text"}); err != nil {
		t.Fatal(err)
	}
	prompt := fx.def.calls[0].prompt
	if !utf8.ValidString(prompt) {
		t.Error("truncation split a UTF-8 rune")
	}
	if !strings.Contains(prompt, "```diff\n"+prefix+"\n```") {
		t.Error("retained text prefix changed bytes")
	}
}

func TestReviewPromptMetadataCannotInjectNonUTF8Stdin(t *testing.T) {
	fx := newReviewFixture(t)
	fx.def.replies = []string{`{"findings":[]}`}
	if _, _, err := runReview(t.Context(), fx.deps, testPolicy(t), reviewInputs{brief: "objective\xff"}); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(fx.def.calls[0].prompt) {
		t.Error("objective made reviewer stdin invalid UTF-8")
	}
}
