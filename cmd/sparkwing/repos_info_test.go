package main

import (
	"strings"
	"testing"
)

func TestRepoSuggestion_LeadsWithGuidesThenDirty(t *testing.T) {
	behind := repoInfo{Pin: "v0.16.0", Latest: "v0.17.0", GuidesBehind: 2}
	if got := repoSuggestion(behind); !strings.Contains(got, "2 guide(s) behind") {
		t.Fatalf("behind suggestion = %q, want the guides bump", got)
	}

	dirty := repoInfo{Pin: "v0.17.0", Latest: "v0.17.0", Dirty: true}
	if got := repoSuggestion(dirty); !strings.Contains(got, "uncommitted") {
		t.Fatalf("dirty suggestion = %q, want the dirty-tree note", got)
	}

	clean := repoInfo{Pin: "v0.17.0", Latest: "v0.17.0"}
	if got := repoSuggestion(clean); got != "" {
		t.Fatalf("clean suggestion = %q, want empty", got)
	}
}
