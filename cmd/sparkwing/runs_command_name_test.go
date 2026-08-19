package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestProductionSourceUsesRunsCommandNames(t *testing.T) {
	retired := regexp.MustCompile(`(?:sparkwing\s+jobs|["'\x60]jobs)\s+(?:list|status|logs|errors|failures|stats|last|tree|get|wait|find|retry|cancel|prune|receipt|grep|summary|timeline)\b`)
	err := filepath.WalkDir("../..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if match := retired.Find(body); match != nil {
			t.Errorf("%s names retired command %q", path, match)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go source: %v", err)
	}
}
