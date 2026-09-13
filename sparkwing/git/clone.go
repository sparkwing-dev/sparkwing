package git

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
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

// Clone clones url into destDir. It tries the local gitcache HTTP cache
// when one is reachable and falls back to upstream on any cache-side
// failure, so a cache can never turn a clone that would work into one
// that does not. destDir must not already exist; matches `git clone`
// semantics.
func Clone(ctx context.Context, url, destDir string, opts ...CloneOption) error {
	cfg := &cloneConfig{}
	for _, o := range opts {
		o(cfg)
	}
	resolved, cache, named := resolveCloneURL(ctx, url)
	args := []string{"clone"}
	if cfg.depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", cfg.depth))
	}
	if cache != "" {
		// safety: Lstat, because a dangling symlink the caller placed would
		// stat as absent and the fallback would then unlink it.
		_, statErr := os.Lstat(destDir)
		preexisting := statErr == nil
		cacheArgs := append(append([]string{}, args...), resolved, destDir)
		_, err := runGitEnv(ctx, "", gitcacheEnv(cache, gitcacheToken(named)), cacheArgs...)
		if err == nil {
			return nil
		}
		// safety: the cache is an optimization, so every failure falls back.
		// Classifying them does not work: the cache answers 404 for a name it
		// does not serve, and git reports that as "repository not found", the
		// same text an upstream miss produces.
		fmt.Fprintf(os.Stderr, "sparkwing: gitcache %s did not serve the clone, cloning %s from upstream: %v\n", cache, url, err)
		if !preexisting {
			if rmErr := os.RemoveAll(destDir); rmErr != nil {
				return fmt.Errorf("clear %s after the gitcache clone failed: %w", destDir, rmErr)
			}
		}
	}
	args = append(args, url, destDir)
	_, err := runGitEnv(ctx, "", promptlessEnv(), args...)
	return err
}

// Fetch runs `git fetch` in repoDir.
func Fetch(ctx context.Context, repoDir string) error {
	_, err := runGitEnv(ctx, repoDir, originEnv(ctx, repoDir), "fetch")
	return err
}

func resolveCloneURL(ctx context.Context, upstream string) (resolved, cache string, named bool) {
	cache, named = detectGitcache(ctx)
	if cache == "" {
		return upstream, "", false
	}
	return cacheCloneURL(cache, upstream), cache, named
}

func originEnv(ctx context.Context, repoDir string) []string {
	cache := configuredGitcache()
	if cache == "" {
		return promptlessEnv()
	}
	origin, err := runGitEnv(ctx, repoDir, promptlessEnv(), "config", "--get", "remote.origin.url")
	if err != nil {
		return promptlessEnv()
	}
	if !strings.HasPrefix(strings.TrimSpace(origin), cache+"/") {
		return promptlessEnv()
	}
	return gitcacheEnv(cache, gitcacheToken(true))
}

func promptlessEnv() []string {
	// safety: git must never stop on a credential prompt; an unattended runner would hang on it.
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}

func gitcacheToken(named bool) string {
	// safety: the bearer goes only to a cache an operator named, never to whatever answered the localhost probe.
	if !named {
		return ""
	}
	return os.Getenv("SPARKWING_CACHE_TOKEN")
}

func gitcacheEnv(cacheBase, token string) []string {
	base := strings.TrimRight(cacheBase, "/") + "/"
	// safety: LC_ALL=C keeps the cache clone's failure text in one language, which is what the fallback notice prints.
	env := append(promptlessEnv(), "LC_ALL=C")
	count, countIndex := 0, -1
	for i, value := range env {
		if rest, ok := strings.CutPrefix(value, "GIT_CONFIG_COUNT="); ok {
			count, countIndex = inheritedConfigCount(rest), i
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

func inheritedConfigCount(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func configuredGitcache() string {
	if v := strings.TrimRight(os.Getenv("SPARKWING_GITCACHE"), "/"); v != "" {
		return v
	}
	return strings.TrimRight(os.Getenv("SPARKWING_GITCACHE_URL"), "/")
}

func detectGitcache(ctx context.Context) (base string, named bool) {
	if v := configuredGitcache(); v != "" {
		return v, true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gitcacheProbeURL+"/health", nil)
	if err != nil {
		return "", false
	}
	client := &http.Client{Timeout: gitcacheProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if !strings.Contains(string(body), "ok") {
		return "", false
	}
	return strings.TrimRight(gitcacheProbeURL, "/"), false
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
