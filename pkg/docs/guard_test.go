package docs_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestPkgDocsContentMatchesDocsRoot(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "docs")
	dst := filepath.Join(root, "pkg", "docs", "mirror")

	srcMap, err := hashTree(src)
	if err != nil {
		t.Fatalf("hash src: %v", err)
	}
	dstMap, err := hashTree(dst)
	if err != nil {
		t.Fatalf("hash dst: %v", err)
	}

	var diffs []string
	seen := map[string]bool{}
	for p, sh := range srcMap {
		seen[p] = true
		dh, ok := dstMap[p]
		if !ok {
			diffs = append(diffs, "missing in pkg/docs/mirror/: "+p)
			continue
		}
		if dh != sh {
			diffs = append(diffs, "content differs: "+p)
		}
	}
	for p := range dstMap {
		if !seen[p] {
			diffs = append(diffs, "stale in pkg/docs/mirror/ (not in docs/): "+p)
		}
	}
	if len(diffs) > 0 {
		sort.Strings(diffs)
		t.Fatalf("pkg/docs/mirror/ is out of sync with docs/:\n  %s\n\n"+
			"Fix: bash bin/sync-docs.sh && git add pkg/docs/mirror/",
			strings.Join(diffs, "\n  "))
	}
}

// hashTree walks a directory and returns relative path -> sha256.
// Map comparison lets failure messages name which files drifted.
func hashTree(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		sum := sha256.Sum256(body)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	return out, err
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for range 6 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate go.mod walking up from test cwd")
	return ""
}
