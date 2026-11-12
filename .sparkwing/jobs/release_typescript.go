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
	for _, name := range []string{"versions.json", "versions.json.sig"} {
		if _, err := sparkwing.Exec(ctx, "curl", "--fail", "--silent", "--show-error", "--location",
			"https://sparkwing.dev/"+name, "-o", filepath.Join(dir, name)).Dir(repo).Run(); err != nil {
			return fmt.Errorf("release TypeScript SDK policy download: %w", err)
		}
	}
	// safety: publication decisions must use the same authenticated policy as the hosted release.
	if _, err := sparkwing.Exec(ctx, "go", "run", "./cmd/verify-release", "--index-out", dir, "--verify").
		Dir(repo).Env("GOWORK", "off").Run(); err != nil {
		return fmt.Errorf("release TypeScript SDK policy verification: %w", err)
	}
	if _, err := sparkwing.Exec(ctx, "node", "bin/typescript-release.mjs", repo,
		filepath.Join(dir, "versions.json"), filepath.Join(dir, "sdk-entry.json"), repo).
		Dir(repo).Env("GITHUB_EVENT_NAME", "").Env("GITHUB_OUTPUT", "").Run(); err != nil {
		return fmt.Errorf("release TypeScript SDK decision: %w", err)
	}
	return nil
}
