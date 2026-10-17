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
	"pkg/controller/handlers.go":  "reporting displays the stored repository metadata without authorizing work",
	"pkg/store/latest_run.go":     "the tenant reporting query filters the sealed repository projection",
	"pkg/store/runfilter_http.go": "the HTTP filter parser selects repository display metadata",
	"pkg/store/store.go":          "the sealed run reporting projection exposes repository metadata without granting authority",
	"pkg/store/teststore/run.go":  "the reporting fixture seeds sealed display metadata without admitting work or granting authority",
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
