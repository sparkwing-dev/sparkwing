package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func trackedProductionGoFiles(repoRoot string, roots ...string) ([]string, error) {
	args := append([]string{"-C", repoRoot, "ls-files", "-z", "--"}, roots...)
	cmd := procenv.ControlCommand(os.Environ(), "git", args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list tracked Go sources: %w", err)
	}
	var paths []string
	for rel := range strings.SplitSeq(string(output), "\x00") {
		if strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") {
			paths = append(paths, filepath.Join(repoRoot, filepath.FromSlash(rel)))
		}
	}
	return paths, nil
}
