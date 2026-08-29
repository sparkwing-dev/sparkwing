package store

import "testing"

func TestProfileKey_RoundTripsSlashBearingComponents(t *testing.T) {
	cases := []struct {
		repo, pipeline string
	}{
		{"github.com/example/acme-service", "build"},
		{"github.com/example/acme-service", "nested/build"},
		{"local:abcdef0123456789abcdef01", "ci"},
		{"sample-repo", "pre-commit"},
	}
	for _, c := range cases {
		key := JoinProfileKey(c.repo, c.pipeline)
		repo, pipeline := SplitProfileKey(key)
		if repo != c.repo || pipeline != c.pipeline {
			t.Errorf("round trip of (%q, %q) through %q gave (%q, %q)", c.repo, c.pipeline, key, repo, pipeline)
		}
	}
}

func TestJoinProfileKey_KeepsBareAndEmptyShapes(t *testing.T) {
	if got := JoinProfileKey("", "ci"); got != "ci" {
		t.Errorf("no repo: got %q, want ci", got)
	}
	if got := JoinProfileKey("alpha", ""); got != "" {
		t.Errorf("no pipeline: got %q, want empty", got)
	}
}

// TestSplitProfileKey_ReadsThePreEncodingShapes keeps rows written by
// earlier releases resolving: pre-v0.37.2 stores hold "repo/pipeline"
// keys and bare names, and a bare name may contain characters that
// merely resemble the encoding.
func TestSplitProfileKey_ReadsThePreEncodingShapes(t *testing.T) {
	cases := []struct {
		key, repo, pipeline string
	}{
		{"myrepo/ci", "myrepo", "ci"},
		{"ci", "", "ci"},
		{"", "", ""},
		{"x:y", "", "x:y"},
		{"+5:alphaci", "", "+5:alphaci"},
		{"99:short", "", "99:short"},
	}
	for _, c := range cases {
		repo, pipeline := SplitProfileKey(c.key)
		if repo != c.repo || pipeline != c.pipeline {
			t.Errorf("SplitProfileKey(%q) = (%q, %q), want (%q, %q)", c.key, repo, pipeline, c.repo, c.pipeline)
		}
	}
}
