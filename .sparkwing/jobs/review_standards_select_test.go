package jobs

import (
	"slices"
	"testing"
)

func TestReviewerCardNamesLoadsReviewCardsByAlwaysScopeAndSurface(t *testing.T) {
	listing := `{"name":"runbook-code-review","mode":"runbook"}
{"name":"standard-fixes","apply_type":["code","review"],"apply_always":true}
{"name":"standard-architecture","apply_type":["api","code","review"]}
{"name":"standard-gates","apply_type":["pipeline","review"]}
{"name":"standard-sparkwing-product","apply_type":["review"]}
{"name":"standard-process","apply_type":["pipeline"],"apply_always":true}
{"name":"standard-other-repo","apply_type":["review"]}
`
	rules := `{"kind":"summary"}
{"kind":"rule","card":"standard-fixes","globs":["**/*.go"]}
{"kind":"rule","card":"standard-architecture","globs":["**/*.go","**/api/**"]}
{"kind":"rule","card":"standard-gates","globs":["**/.sparkwing/**","**/.golangci*"]}
{"kind":"rule","card":"standard-sparkwing-product","globs":[]}
{"kind":"rule","card":"standard-process","globs":["**/.sparkwing/**"]}
`
	for _, tc := range []struct {
		files []string
		want  []string
	}{
		{[]string{"internal/controller/server.go"}, []string{"standard-fixes", "standard-architecture", "standard-sparkwing-product"}},
		{[]string{".sparkwing/sparkwing.yaml"}, []string{"standard-fixes", "standard-gates", "standard-sparkwing-product"}},
		{[]string{".golangci.yml"}, []string{"standard-fixes", "standard-gates", "standard-sparkwing-product"}},
		{[]string{"README.txt"}, []string{"standard-fixes", "standard-sparkwing-product"}},
	} {
		if got := reviewerCardNames(listing, rules, tc.files); !slices.Equal(got, tc.want) {
			t.Errorf("reviewerCardNames(%q) = %q, want %q", tc.files, got, tc.want)
		}
	}
}
