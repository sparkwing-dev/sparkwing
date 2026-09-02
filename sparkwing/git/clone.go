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
	resolved, cache := resolveCloneURL(ctx, url)
	args := []string{"clone"}
	if cfg.depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", cfg.depth))
	}
	if cache != "" {
		cacheArgs := append(append([]string{}, args...), resolved, destDir)
		_, err := runGitEnv(ctx, "", gitcacheEnv(cache, os.Getenv("SPARKWING_CACHE_TOKEN")), cacheArgs...)
		if err == nil {
			return nil
		}
		if !rejectedByCache(err) {
			return err
		}
		// safety: a rejected clone can leave destDir behind, and the upstream retry needs it absent.
		if rmErr := os.RemoveAll(destDir); rmErr != nil {
			return rmErr
		}
	}
	args = append(args, url, destDir)
	_, err := runGit(ctx, "", args...)
	return err
}

// Fetch runs `git fetch` in repoDir.
func Fetch(ctx context.Context, repoDir string) error {
	_, err := runGit(ctx, repoDir, "fetch")
	return err
}

func resolveCloneURL(ctx context.Context, upstream string) (resolved, cache string) {
	cache = detectGitcache(ctx)
	if cache == "" {
		return upstream, ""
	}
	return cacheCloneURL(cache, upstream), cache
}

func gitcacheEnv(cacheBase, token string) []string {
	base := strings.TrimRight(cacheBase, "/") + "/"
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	count, countIndex := 0, -1
	for i, value := range env {
		if rest, ok := strings.CutPrefix(value, "GIT_CONFIG_COUNT="); ok {
			_, _ = fmt.Sscanf(rest, "%d", &count)
			countIndex = i
		}
	}
	entries := 1
	if token != "" {
		entries++
	}
	countValue := fmt.Sprintf("GIT_CONFIG_COUNT=%d", count+entries)
	if countIndex >= 0 {
		env[countIndex] = countValue
	} else {
		env = append(env, countValue)
	}
	if token != "" {
		// safety: the bearer travels in the environment so it never lands on the command line.
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=http.%s.extraHeader", count, base),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=Authorization: Bearer %s", count, token),
		)
		count++
	}
	// safety: redirects stay off so the bearer cannot follow the request to another host.
	env = append(env,
		fmt.Sprintf("GIT_CONFIG_KEY_%d=http.%s.followRedirects", count, base),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=false", count),
	)
	return env
}

func rejectedByCache(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"returned error: 401",
		"authentication failed",
		"could not read username",
		"could not read password",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
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
