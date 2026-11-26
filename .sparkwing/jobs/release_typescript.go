package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func checkTypeScriptRelease(ctx context.Context, repo string) error {
	return withSDKPolicyScratch(ctx, func(dir string) error {
		if err := downloadVerifiedReleaseIndex(ctx, repo, dir); err != nil {
			return fmt.Errorf("release TypeScript SDK policy: %w", err)
		}
		return decideTypeScriptRelease(ctx, repo, dir)
	})
}

func checkTypeScriptSDKVersion(ctx context.Context) error {
	inputs, scope, err := changeScope(ctx, "SDK release input(s)", typeScriptReleaseInputs)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		sparkwing.Info(ctx, "sdk-version: skipping, because the change holds no SDK release input (%s)", scope)
		return nil
	}
	repo := regexCheckRoot()
	return withSDKPolicyScratch(ctx, func(dir string) error {
		// safety: an outage the change cannot clear must not block it; the release
		// cut's typescript-release check still refuses an unverified index.
		if err := downloadVerifiedReleaseIndex(ctx, repo, dir); err != nil {
			sparkwing.Warn(ctx, "sdk-version: UNVERIFIED: the published SDK index is unavailable, so the SDK version was not judged (%s); the release cut repeats this check: %v", scope, err)
			return nil
		}
		return decideTypeScriptRelease(ctx, repo, dir)
	})
}

// safety: these are the paths sdkDigest reads plus the digest code itself; an
// input missing here would skip the check for a change that needs a bump.
func typeScriptReleaseInputs(files []string) []string {
	var inputs []string
	for _, f := range files {
		if strings.HasPrefix(f, "sdk/typescript/") || f == ".github/workflows/release.yaml" || f == "bin/typescript-release.mjs" {
			inputs = append(inputs, f)
		}
	}
	return inputs
}

func decideTypeScriptRelease(ctx context.Context, repo, dir string) error {
	if _, err := sparkwing.Exec(ctx, "node", "bin/typescript-release.mjs", repo,
		filepath.Join(dir, "versions.json"), filepath.Join(dir, "sdk-entry.json"), repo).
		Dir(repo).Env("GITHUB_EVENT_NAME", "").Env("GITHUB_OUTPUT", "").Run(); err != nil {
		return fmt.Errorf("TypeScript SDK release decision: %w", err)
	}
	return nil
}

func withSDKPolicyScratch(ctx context.Context, run func(dir string) error) error {
	dir, err := os.MkdirTemp("", "sparkwing-sdk-policy-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			sparkwing.Warn(ctx, "remove SDK policy scratch: %v", err)
		}
	}()
	return run(dir)
}
