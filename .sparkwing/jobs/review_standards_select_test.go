package jobs

import (
	"slices"
	"testing"
)

func TestReviewerCardNamesSelectsReviewAndAlwaysCardsInScope(t *testing.T) {
	listing := `{"name":"runbook-code-review","mode":"runbook"}
{"name":"standard-comments","mode":"always,type","apply_type":["code","review"],"apply_always":true}
{"name":"standard-testing","mode":"type","apply_type":["test","review"]}
{"name":"standard-ui-design","mode":"type","apply_type":["ui"]}
{"name":"standard-consumers","mode":"type","apply_type":["api","review"],"scope_repo":["sparkwing"]}
{"name":"standard-other-repo","mode":"type","apply_type":["review"],"scope_repo":["elsewhere"]}
{"name":"standard-landing","mode":"always","apply_always":true,"scope_owner":["koreyGambill"]}
{"name":"standard-manual","mode":"manual"}
`
	got := reviewerCardNames(listing, "sparkwing-dev", "sparkwing")
	want := []string{"standard-comments", "standard-testing", "standard-consumers"}
	if !slices.Equal(got, want) {
		t.Fatalf("reviewerCardNames = %q, want %q", got, want)
	}
	if got := reviewerCardNames(listing, "koreyGambill", "sparkwing"); !slices.Contains(got, "standard-landing") {
		t.Fatalf("owner-scoped card missing for its owner: %q", got)
	}
}

func TestRepoOwnerAndNameReadsOriginForms(t *testing.T) {
	for _, tc := range []struct{ origin, owner, repo string }{
		{"git@github.com:sparkwing-dev/sparkwing", "sparkwing-dev", "sparkwing"},
		{"git@github.com:sparkwing-dev/sparkwing.git", "sparkwing-dev", "sparkwing"},
		{"https://github.com/koreyGambill/kikd-infra.git", "koreyGambill", "kikd-infra"},
		{"", "", "checkout"},
	} {
		owner, repo := repoOwnerAndName(tc.origin, "/tmp/checkout")
		if owner != tc.owner || repo != tc.repo {
			t.Errorf("repoOwnerAndName(%q) = %q, %q; want %q, %q", tc.origin, owner, repo, tc.owner, tc.repo)
		}
	}
}
