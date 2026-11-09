package jobs

import (
	"os"
	"path/filepath"

	docs "github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func readChangelog(repoDir string) ([]byte, error) {
	body, err := os.ReadFile(filepath.Join(repoDir, "CHANGELOG.md"))
	if err != nil {
		return nil, err
	}
	collected, err := docs.CollectChangelog(string(body), os.DirFS(filepath.Join(repoDir, "docs")))
	return []byte(collected), err
}

var releaseFragmentDirs = []string{
	"docs/changes", "pkg/docs/mirror/changes",
	"docs/migrations/unreleased", "pkg/docs/mirror/migrations/unreleased",
}

func consumeReleaseFragments(repoDir string) ([]string, error) {
	var consumed []string
	for _, dir := range releaseFragmentDirs {
		paths, err := filepath.Glob(filepath.Join(repoDir, dir, "*.md"))
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			rel, err := filepath.Rel(repoDir, path)
			if err != nil {
				return nil, err
			}
			consumed = append(consumed, rel)
		}
	}
	return consumed, nil
}
