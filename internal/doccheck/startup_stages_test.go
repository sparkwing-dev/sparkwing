package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCloudStartupStagesAreDocumented(t *testing.T) {
	root := filepath.Join("..", "..")
	docs, err := os.ReadFile(filepath.Join(root, "docs", "observability.md"))
	if err != nil {
		t.Fatal(err)
	}
	stageRE := regexp.MustCompile(`"(?:bootstrap|launcher)_[a-z_]+"`)
	count := 0
	for _, file := range []string{"internal/cluster/production_runtime.go", "internal/runners/hosting.go", "internal/runners/launcher/bootstrap.go", "internal/runners/launcher/launcher.go", "internal/runners/launcher/capacity.go"} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		for _, stage := range stageRE.FindAllString(string(body), -1) {
			count++
			if !strings.Contains(string(docs), "`"+strings.Trim(stage, `"`)+"`") {
				t.Errorf("%s stage %s missing from observability docs", file, stage)
			}
		}
	}
	if count == 0 {
		t.Fatal("no Cloud startup stages found")
	}
}
