package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The node/trigger/run lifecycle vocabulary lives in lifecycle.go;
// Go-side queries reference statuses through those constants or the
// canonical fragments, never as fresh inline literals. The schema DDL
// keeps a fixed number of literal copies (column defaults and partial
// indexes can't reference Go constants); these counts pin them so a
// new inline literal -- the seed of the next untwinned guard -- fails
// the suite instead of merging quietly.
func TestLifecycleGuard_StatusLiteralsStayCanonical(t *testing.T) {
	root := moduleRoot(t)
	storeSrc := storePackageSource(t)

	for needle, allowed := range map[string]int{
		// lifecycle.go (nodeNotDone) + the nodes partial index DDL.
		"status != 'done'": 2,
		// lifecycle.go (nodeFailSet) only.
		"status = 'done', outcome = 'failed'": 1,
		// Trigger partial-index DDL only.
		"WHERE status = 'pending'": 1,
		"WHERE status = 'claimed'": 1,
	} {
		if got := strings.Count(storeSrc, needle); got != allowed {
			t.Errorf("%q appears %d times in pkg/store sources, want %d (lifecycle.go fragments + schema DDL); route new sites through lifecycle.go", needle, got, allowed)
		}
	}

	// No package outside pkg/store writes lifecycle statuses into the
	// store's tables directly.
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "dist" || name == "web" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasPrefix(path, filepath.Join(root, "pkg", "store")+string(filepath.Separator)) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		for _, form := range []string{"UPDATE nodes", "UPDATE triggers", "UPDATE runs"} {
			if strings.Contains(s, form) {
				t.Errorf("%s contains %q: lifecycle tables may only be written by pkg/store", path, form)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
