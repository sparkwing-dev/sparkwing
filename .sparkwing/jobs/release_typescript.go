package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func checkTypeScriptRelease(ctx context.Context, repo string) error {
	dir, err := os.MkdirTemp("", "sparkwing-sdk-policy-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			sparkwing.Warn(ctx, "remove SDK policy scratch: %v", err)
		}
	}()
	if err := downloadVerifiedReleaseIndex(ctx, repo, dir); err != nil {
		return fmt.Errorf("release TypeScript SDK policy: %w", err)
	}
	if _, err := sparkwing.Exec(ctx, "node", "bin/typescript-release.mjs", repo,
		filepath.Join(dir, "versions.json"), filepath.Join(dir, "sdk-entry.json"), repo).
		Dir(repo).Env("GITHUB_EVENT_NAME", "").Env("GITHUB_OUTPUT", "").Run(); err != nil {
		return fmt.Errorf("release TypeScript SDK decision: %w", err)
	}
	return nil
}
