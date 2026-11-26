package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewDeletingABinaryNeedsAPersonOnlyOnAStrictPath(t *testing.T) {
	blob := "\x00\x01\x02profile\x00data"
	for _, tc := range []struct {
		path  string
		block bool
	}{
		{"design/results/sdk-P2.prof", false},
		{"internal/store/billing_hidden.bin", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			fx, root := gitEncodingReviewFixture(t, map[string]string{tc.path: blob}, map[string]string{"other.md": "note\n"})
			if err := os.Remove(filepath.Join(root, tc.path)); err != nil {
				t.Fatal(err)
			}
			gitCommitAll(t, root, "delete the binary")
			rec, _, err := runReview(newTestContext(t), fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Delete a binary"})
			if err != nil {
				t.Fatal(err)
			}
			flagged := false
			for _, f := range rec.Findings {
				flagged = flagged || strings.Contains(f.Summary, "binary or unreadable change requires human review: "+tc.path)
			}
			if flagged != tc.block {
				t.Fatalf("deleting %s: human-review finding = %v, want %v; findings %+v", tc.path, flagged, tc.block, rec.Findings)
			}
			if !tc.block && rec.Result != resultPass {
				t.Fatalf("deleting %s should land: %+v", tc.path, rec)
			}
		})
	}
}
