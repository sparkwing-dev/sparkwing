package bincache

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var bundleCommitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// safety: bundles must not redirect pipeline lookup through a symlink.
func bundlePipelineDir(checkout string) (string, error) {
	candidate := filepath.Join(checkout, ".sparkwing")
	fi, err := os.Lstat(candidate)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("fetched tree has no .sparkwing directory under %s", checkout)
	}
	realCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return "", fmt.Errorf("direct source: %w", err)
	}
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("direct source: %w", err)
	}
	if rel, relErr := filepath.Rel(realCheckout, realCandidate); relErr != nil || rel != ".sparkwing" {
		return "", fmt.Errorf("fetched .sparkwing resolves outside the checkout %s", checkout)
	}
	return candidate, nil
}

func bundleGitEnv(base []string) []string {
	out := make([]string, 0, len(base)+3)
	for _, item := range base {
		name, _, _ := strings.Cut(item, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_ALLOW_PROTOCOL",
			"GIT_CURL_VERBOSE", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM",
			"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT":
			continue
		}
		if strings.HasPrefix(name, "GIT_TRACE") || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			continue
		}
		out = append(out, item)
	}
	return append(out, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_LFS_SKIP_SMUDGE=1")
}
