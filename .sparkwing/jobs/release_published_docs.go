package jobs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"golang.org/x/mod/semver"
)

// publishedDocsIndexURL is the public docs index; its title names the release
// the site documents. Tests point it at a local server.
var publishedDocsIndexURL = "https://sparkwing.dev/llms.txt"

var publishedDocsVersionRe = regexp.MustCompile(`(?m)^# sparkwing -- (v[0-9]+\.[0-9]+\.[0-9]+) \(latest\)`)

// safety: publishing a release's docs is a separate sparkwing-product push that
// nothing else checks, so refusing the next cut bounds a skipped one to one release.
func checkPublishedDocs(ctx context.Context, repoDir string) error {
	released, err := latestSemverTagIn(ctx, repoDir)
	if err != nil {
		return fmt.Errorf("published docs: list release tags: %w", err)
	}
	if released == "" {
		return nil
	}
	return checkPublishedDocsName(ctx, publishedDocsIndexURL, released)
}

func checkPublishedDocsName(ctx context.Context, indexURL, released string) error {
	documented, err := publishedDocsVersion(ctx, indexURL)
	if err != nil {
		return fmt.Errorf("published docs: %w", err)
	}
	if semver.Compare(documented, released) < 0 {
		return fmt.Errorf(
			"published docs: %s documents %s but %s is released; publish %s's sparkwing-product docs (add it to versions.json as latest and push product main) before cutting another release",
			indexURL, documented, released, released,
		)
	}
	return nil
}

func publishedDocsVersion(ctx context.Context, indexURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", indexURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("read %s: status %d", indexURL, resp.StatusCode)
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", indexURL, err)
	}
	m := publishedDocsVersionRe.FindSubmatch(head)
	if m == nil {
		return "", fmt.Errorf("%s does not name its latest documented release in a %q title", indexURL, "# sparkwing -- vX.Y.Z (latest)")
	}
	return string(m[1]), nil
}
