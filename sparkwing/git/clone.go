package git

import (
	"context"
	"fmt"
	"os"
	"runtime"
)

// CloneOption configures a clone.
type CloneOption func(*cloneConfig)
type cloneConfig struct{ depth int }

// WithDepth limits the fetched history to n commits.
func WithDepth(n int) CloneOption { return func(c *cloneConfig) { c.depth = n } }

// Clone acquires url directly into a new destination directory.
func Clone(ctx context.Context, url, destDir string, opts ...CloneOption) error {
	cfg := cloneConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.depth < 0 {
		return fmt.Errorf("clone depth must not be negative")
	}
	args := []string{"clone"}
	if cfg.depth > 0 {
		args = append(args, "--depth", fmt.Sprint(cfg.depth))
	}
	args = append(args, "--", url, destDir)
	_, err := runGitEnv(ctx, "", promptlessEnv(), args...)
	return err
}

// Fetch updates the configured origin directly.
func Fetch(ctx context.Context, repoDir string) error {
	_, err := runGitEnv(ctx, repoDir, promptlessEnv(), "fetch")
	return err
}

func promptlessEnv() []string {
	// safety: unattended jobs cannot answer terminal or desktop credential prompts.
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if runtime.GOOS == "windows" {
		env = append(env, "GCM_INTERACTIVE=0")
	}
	return env
}
