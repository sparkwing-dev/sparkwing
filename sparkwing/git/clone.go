package git

import (
	"context"
	"os"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/internal/gitcmd"
)

// CloneOption configures a clone.
type (
	CloneOption func(*cloneConfig)
	cloneConfig struct{ depth int }
)

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
	return gitcmd.CloneRepository(ctx, url, destDir, cfg.depth, promptlessEnv())
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
