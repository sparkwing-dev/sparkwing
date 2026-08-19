package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionCommentsUseCurrentConfigFilename(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	for _, root := range []string{"cmd", "internal", "pkg", "sparkwing"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, group := range file.Comments {
				for _, comment := range group.List {
					if strings.Contains(comment.Text, "pipelines.yaml") {
						t.Errorf("%s names the removed pipelines.yaml config", fset.Position(comment.Pos()))
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}
}
