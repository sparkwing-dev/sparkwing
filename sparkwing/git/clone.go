package git

import (
	"context"
	"fmt"
	"os"
	"runtime"
)

// CloneOption configures optional git-clone behavior.
type CloneOption func(*cloneConfig)

type cloneConfig struct{ depth int }

// WithDepth limits the clone to the most recent n commits (--depth n).
func WithDepth(n int) CloneOption {
	return func(c *cloneConfig) { c.depth = n }
}

// Clone clones url directly into destDir using the caller's git credentials.
func Clone(ctx context.Context, url, destDir string, opts ...CloneOption) error {
	cfg := &cloneConfig{}
	for _, o := range opts {
		o(cfg)
	}
	args := []string{"clone"}
	if cfg.depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", cfg.depth))
	}
	args = append(args, "--", url, destDir)
	_, err := runGitEnv(ctx, "", promptlessEnv(), args...)
	return err
}

// Fetch runs git fetch against the checkout's configured remotes.
func Fetch(ctx context.Context, repoDir string) error {
	_, err := runGitEnv(ctx, repoDir, promptlessEnv(), "fetch")
	return err
}

func promptlessEnv() []string {
	// safety: unattended jobs cannot answer a credential prompt.
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if runtime.GOOS == "windows" {
		env = append(env, "GCM_INTERACTIVE=0")
	}
	return env
}
