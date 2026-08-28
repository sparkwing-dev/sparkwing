package git

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var gitcacheProbeURL = "http://localhost:18090"

var gitcacheProbeTimeout = 200 * time.Millisecond

// CloneOption configures optional git-clone behavior.
type CloneOption func(*cloneConfig)

type cloneConfig struct {
	depth int
}

// WithDepth limits the clone to the most recent n commits (--depth n).
func WithDepth(n int) CloneOption {
	return func(c *cloneConfig) { c.depth = n }
}

// Clone clones url into destDir. Routes through the local gitcache
// HTTP cache when reachable, falling back transparently to upstream.
// destDir must not already exist; matches `git clone` semantics.
func Clone(ctx context.Context, url, destDir string, opts ...CloneOption) error {
	cfg := &cloneConfig{}
	for _, o := range opts {
		o(cfg)
	}
	resolved := resolveCloneURL(ctx, url)
	args := []string{"clone"}
	if cfg.depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", cfg.depth))
	}
	args = append(args, resolved, destDir)
	_, err := runGit(ctx, "", args...)
	return err
}

// Fetch runs `git fetch` in repoDir.
func Fetch(ctx context.Context, repoDir string) error {
	_, err := runGit(ctx, repoDir, "fetch")
	return err
}

func resolveCloneURL(ctx context.Context, upstream string) string {
	cache := detectGitcache(ctx)
	if cache == "" {
		return upstream
	}
	return cacheCloneURL(cache, upstream)
}

func detectGitcache(ctx context.Context) string {
	if v := strings.TrimRight(os.Getenv("SPARKWING_GITCACHE"), "/"); v != "" {
		return v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gitcacheProbeURL+"/health", nil)
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: gitcacheProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if !strings.Contains(string(body), "ok") {
		return ""
	}
	return strings.TrimRight(gitcacheProbeURL, "/")
}

func cacheCloneURL(cacheBase, upstream string) string {
	repoName := upstream
	if i := strings.LastIndex(repoName, "/"); i >= 0 {
		repoName = repoName[i+1:]
	} else if i := strings.LastIndex(repoName, ":"); i >= 0 {
		repoName = repoName[i+1:]
	}
	repoName = strings.TrimSuffix(repoName, ".git")
	return strings.TrimRight(cacheBase, "/") + "/git/" + repoName
}
