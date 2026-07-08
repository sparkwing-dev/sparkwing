package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var failureConstRE = regexp.MustCompile(`Failure\w+\s*=\s*"([a-z_]+)"`)

// checkFailureReasons keeps docs/observability.md in sync with the
// canonical failure-reason set. The reasons are a single clean enum
// (pkg/store: Failure* constants); the doc's per-reason guidance is
// hand-written and stays so, but the *set* must be complete -- a new
// reason added to the store without a doc row fails here. (This is the
// gate that would have caught observability.md inventing `pod_error`
// and omitting `verify`/`runner_lease_expired`/`logs_auth`.)
func checkFailureReasons(contentDir, repoRoot string) bool {
	store, err := os.ReadFile(filepath.Join(repoRoot, "pkg", "store", "store.go"))
	if err != nil {
		fmt.Println("failure-reasons: read store.go:", err)
		return false
	}
	doc, err := os.ReadFile(filepath.Join(contentDir, "observability.md"))
	if err != nil {
		fmt.Println("failure-reasons: read observability.md:", err)
		return false
	}
	docText := string(doc)

	var reasons, missing []string
	for _, m := range failureConstRE.FindAllStringSubmatch(string(store), -1) {
		code := m[1]
		if code == "" {
			continue
		}
		reasons = append(reasons, code)
		if !strings.Contains(docText, "`"+code+"`") {
			missing = append(missing, code)
		}
	}

	fmt.Printf("doccheck/failure-reasons: %d Failure* constant(s) -- %d undocumented\n",
		len(reasons), len(missing))
	if len(missing) > 0 {
		fmt.Printf("\nfailure reasons defined in pkg/store but not documented in observability.md:\n")
		for _, c := range missing {
			fmt.Println("  " + c)
		}
		return false
	}
	fmt.Println("\nALL FAILURE REASONS DOCUMENTED")
	return true
}
