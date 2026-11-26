package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const publicSiteURL = "https://sparkwing.dev/"

// safety: the product site's llms.txt generator writes exactly this first line.
var publishedDocsTitleRe = regexp.MustCompile(`^# sparkwing -- (v\S+) \(latest\)$`)

// safety: the baseline is the signed index's latest stable release, written only after
// a GitHub release publishes, so a failed hosted build, a prerelease or an unpushed tag
// never demands docs that cannot publish yet, and the repairing patch cut stays possible.
func checkPublishedDocs(ctx context.Context, repo string) error {
	dir, err := os.MkdirTemp("", "sparkwing-published-docs-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			sparkwing.Warn(ctx, "remove published docs scratch: %v", err)
		}
	}()
	// safety: the title is the first line of a ~35 KiB file, so fetch only its head.
	if err := downloadPublicSiteFile(ctx, repo, dir, "llms.txt", "--range", "0-4095"); err != nil {
		return fmt.Errorf("published docs: %w", err)
	}
	llms, err := os.ReadFile(filepath.Join(dir, "llms.txt"))
	if err != nil {
		return fmt.Errorf("published docs: %w", err)
	}
	documented, err := documentedRelease(llms)
	if err != nil {
		return fmt.Errorf("published docs: %w", err)
	}
	if err := downloadVerifiedReleaseIndex(ctx, repo, dir); err != nil {
		return fmt.Errorf("published docs: %w", err)
	}
	published, err := latestPublishedRelease(filepath.Join(dir, "versions.json"))
	if err != nil {
		return fmt.Errorf("published docs: %w", err)
	}
	if semver.Compare(documented, published) < 0 {
		return fmt.Errorf(
			"published docs: %sllms.txt documents %s but %s is the latest published release; publish %s's docs to the product site (add it to versions.json as latest and push the site's main) before cutting another release",
			publicSiteURL, documented, published, published,
		)
	}
	return nil
}

func documentedRelease(llms []byte) (string, error) {
	first, _, _ := bytes.Cut(llms, []byte("\n"))
	m := publishedDocsTitleRe.FindSubmatch(first)
	if m == nil || !releaseVersion(string(m[1])) {
		return "", fmt.Errorf("%sllms.txt must begin with %q, got %q", publicSiteURL, "# sparkwing -- vX.Y.Z (latest)", first)
	}
	return string(m[1]), nil
}

func latestPublishedRelease(indexPath string) (string, error) {
	body, err := os.ReadFile(indexPath)
	if err != nil {
		return "", err
	}
	var index struct {
		Latest string `json:"latest"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		return "", fmt.Errorf("read the release index: %w", err)
	}
	if !releaseVersion(index.Latest) {
		return "", fmt.Errorf("the release index names %q as latest, not a stable vX.Y.Z release", index.Latest)
	}
	return index.Latest, nil
}

func releaseVersion(v string) bool {
	return semver.IsValid(v) && semver.Canonical(v) == v && semver.Prerelease(v) == ""
}

// safety: a stalled response must fail the check, not hold the cut until its job timeout.
const publicSiteFetchSeconds = "30"

func downloadPublicSiteFile(ctx context.Context, repo, dir, name string, extra ...string) error {
	args := append([]string{"--fail", "--silent", "--show-error", "--location", "--max-time", publicSiteFetchSeconds}, extra...)
	args = append(args, publicSiteURL+name, "-o", filepath.Join(dir, name))
	if _, err := sparkwing.Exec(ctx, "curl", args...).Dir(repo).Run(); err != nil {
		return fmt.Errorf("download %s%s: %w", publicSiteURL, name, err)
	}
	return nil
}

func downloadVerifiedReleaseIndex(ctx context.Context, repo, dir string) error {
	for _, name := range []string{"versions.json", "versions.json.sig"} {
		if err := downloadPublicSiteFile(ctx, repo, dir, name); err != nil {
			return err
		}
	}
	// safety: publication decisions must use the same authenticated policy as the hosted release.
	if _, err := sparkwing.Exec(ctx, "go", "run", "./cmd/verify-release", "--index-out", dir, "--verify").
		Dir(repo).Env("GOWORK", "off").Run(); err != nil {
		return fmt.Errorf("verify the release index: %w", err)
	}
	return nil
}
