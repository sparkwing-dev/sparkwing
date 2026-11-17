package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

//sparkwing:critical
func TestProductionCommentsUseCurrentConfigFilename(t *testing.T) {
	if err := checkProductionComments(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
}

func checkProductionComments(repoRoot string) error {
	paths, err := trackedProductionGoFiles(repoRoot, "cmd", "internal", "pkg", "sparkwing")
	if err != nil {
		return err
	}
	for _, path := range paths {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, group := range file.Comments {
			for _, comment := range group.List {
				if strings.Contains(comment.Text, "pipelines.yaml") {
					return fmt.Errorf("%s names the removed pipelines.yaml config", fset.Position(comment.Pos()))
				}
			}
		}
	}
	return nil
}
