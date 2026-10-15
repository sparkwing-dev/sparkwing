package store_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// safety: a run's declared repository is whatever its submitter typed, so no
// authorization may read it. Every file naming the field is listed with its
// reason, because a new reader has to be argued for in a diff.
var declaredRepoReaders = map[string]string{
	"pkg/store/store.go":                     "the column's own persistence: schema, insert, scan, and the list filter",
	"pkg/store/runfilter_http.go":            "parses the ?repo= list filter off a request",
	"pkg/store/run_filter_display.go":        "matches the run list's repository name and text filters, in SQL and in memory",
	"pkg/controller/handlers.go":             "copies the trigger's repository onto the run row for display",
	"pkg/controller/github_app_webhook.go":   "copies the installation's repository onto the pending run row for display",
	"pkg/controller/client/client.go":        "sends the ?repo= list filter",
	"internal/nodehost/scheduler.go":         "records the repository the run was started from",
	"internal/orchestrator/backends.go":      "copies a parent run's repository onto a child trigger",
	"internal/orchestrator/pod_node_host.go": "hands the repository to plan code in the run context",
	"internal/orchestrator/trigger_host.go":  "hands plan code the repository the run row records, as every node host of the run reads it",
	"pkg/store/child_invocations.go":         "copies a child trigger's repository onto the child's run row",
	"internal/orchestrator/runs_page.go":     "passes the operator's runs list --repo filter through",
	"cmd/sparkwing/repos.go":                 "groups runs by repository for display",
	"cmd/sparkwing/repos_info.go":            "matches runs to a repository for display",
	"cmd/sparkwing/run_detached.go":          "records the repository a detached run was started from",
}

var declaredRepoPattern = regexp.MustCompile(`\bDeclaredRepos?\b|\bdeclared_repo\b`)

func TestDeclaredRepoGuard_OnlyReviewedFilesReadTheField(t *testing.T) {
	root := moduleRoot(t)
	found := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "dist" || name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !declaredRepoPattern.Match(b) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		found[rel] = true
		if _, ok := declaredRepoReaders[rel]; !ok {
			t.Errorf("%s reads a run's declared repository and is not in declaredRepoReaders; "+
				"add it with the reason it may, or route the decision through something proven", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(found) == 0 {
		t.Fatal("no file reads the declared repository; the guard would pass vacuously")
	}
	var stale []string
	for rel, reason := range declaredRepoReaders {
		if reason == "" {
			t.Errorf("declaredRepoReaders[%q] carries no reason", rel)
		}
		if !found[rel] {
			stale = append(stale, rel)
		}
	}
	sort.Strings(stale)
	for _, rel := range stale {
		t.Errorf("declaredRepoReaders lists %q, which no longer reads the field; remove the entry", rel)
	}
	if len(found) != len(declaredRepoReaders) {
		t.Errorf("declaredRepoReaders holds %d entries and %d files read the field",
			len(declaredRepoReaders), len(found))
	}
}
