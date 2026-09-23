package sourceurl

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Identity is the host and path a clone URL names, lowercased and without a
// trailing ".git" or slash, so every spelling of one repository -- https,
// scp-like ssh and ssh:// -- reads the same: "github.com/acme/app". A port is
// kept as part of the host because it names a different server.
func Identity(raw string) (string, error) {
	validated, err := ValidateCloneURL(raw)
	if err != nil {
		return "", err
	}
	var host, repoPath string
	if match := scpLikeRE.FindStringSubmatch(validated); match != nil {
		host, repoPath = match[1], match[2]
	} else {
		u, perr := url.Parse(validated)
		if perr != nil {
			return "", fmt.Errorf("parse repo URL: %w", perr)
		}
		// safety: git drops neither, so two URLs that differ only here would name
		// one repository for matching and another for the fetch.
		if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(validated, "#") {
			return "", errors.New("repo URL must not carry a query string or fragment")
		}
		host, repoPath = u.Host, u.Path
	}
	host = strings.TrimRight(strings.ToLower(host), ".")
	repoPath = strings.ToLower(strings.Trim(repoPath, "/"))
	repoPath = strings.Trim(strings.TrimSuffix(repoPath, ".git"), "/")
	if repoPath == "" || strings.Contains(repoPath, "//") || hasDotSegment(repoPath) {
		return "", fmt.Errorf("repo URL path %q does not name a repository", repoPath)
	}
	return host + "/" + repoPath, nil
}

func hasDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

var githubSlugRE = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// GitHubIdentity is the Identity of the GitHub repository an "owner/name" slug
// names.
func GitHubIdentity(slug string) (string, error) {
	if !githubSlugRE.MatchString(slug) {
		return "", fmt.Errorf("GitHub repository %q is not an owner/name slug", slug)
	}
	return Identity("https://github.com/" + slug)
}

// TriggerRepository is the one repository a trigger names, as an Identity, or
// "" when it names none. A trigger can name its repository three ways -- the
// clone URL, GITHUB_REPOSITORY and github_owner/github_repo -- and the run page,
// the commit status and the runner's fetch each read a different one, so every
// one given must name the same repository or the trigger is refused.
func TriggerRepository(repoURL, githubRepository, githubOwner, githubRepo string) (string, error) {
	type named struct{ field, identity string }
	var names []named
	if repoURL != "" {
		id, err := Identity(repoURL)
		if err != nil {
			return "", fmt.Errorf("git.repo_url: %w", err)
		}
		names = append(names, named{"git.repo_url", id})
	}
	if githubRepository != "" {
		id, err := GitHubIdentity(githubRepository)
		if err != nil {
			return "", fmt.Errorf("GITHUB_REPOSITORY: %w", err)
		}
		names = append(names, named{"GITHUB_REPOSITORY", id})
	}
	if githubOwner != "" || githubRepo != "" {
		id, err := GitHubIdentity(githubOwner + "/" + githubRepo)
		if err != nil {
			return "", fmt.Errorf("github_owner/github_repo: %w", err)
		}
		names = append(names, named{"github_owner/github_repo", id})
	}
	if len(names) == 0 {
		return "", nil
	}
	for _, n := range names[1:] {
		if n.identity != names[0].identity {
			return "", fmt.Errorf("%s names %s but %s names %s; a trigger names one repository",
				names[0].field, names[0].identity, n.field, n.identity)
		}
	}
	return names[0].identity, nil
}

// RepoAllowlist is the set of repositories a machine's owner lets it build.
// Each pattern is an Identity in which '*' matches within one path segment:
// "github.com/acme/*" admits github.com/acme/app but not github.com/acme/app/sub
// or github.com/other/app. The zero value admits nothing.
type RepoAllowlist struct {
	patterns []string
}

var repoPatternRE = regexp.MustCompile(`^[a-z0-9._*/:-]+$`)

// ParseRepoPattern checks one allowlist pattern and returns its canonical form.
// It is a host and path with no scheme, the host has no wildcard, and at least
// one path segment follows it.
func ParseRepoPattern(raw string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(raw))
	p = strings.Trim(strings.TrimSuffix(strings.TrimRight(p, "/"), ".git"), "/")
	if p == "" {
		return "", errors.New("repository pattern is empty")
	}
	if strings.Contains(p, "://") || strings.Contains(p, "@") {
		return "", fmt.Errorf("repository pattern %q is a host and path such as github.com/acme/*, with no scheme or user", raw)
	}
	if !repoPatternRE.MatchString(p) {
		return "", fmt.Errorf("repository pattern %q may hold only letters, digits, '.', '_', '-', ':', '/' and '*'", raw)
	}
	host, rest, ok := strings.Cut(p, "/")
	if !ok || rest == "" {
		return "", fmt.Errorf("repository pattern %q names no path after the host", raw)
	}
	if strings.Contains(host, "*") || !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return "", fmt.Errorf("repository pattern %q must start with a literal host such as github.com", raw)
	}
	if strings.Contains(p, "//") || hasDotSegment(rest) {
		return "", fmt.Errorf("repository pattern %q has an empty or dot path segment", raw)
	}
	if _, err := path.Match(p, ""); err != nil {
		return "", fmt.Errorf("repository pattern %q: %w", raw, err)
	}
	return p, nil
}

// ParseRepoAllowlist parses every pattern, refusing the list at the first bad one.
func ParseRepoAllowlist(patterns []string) (RepoAllowlist, error) {
	var out RepoAllowlist
	for _, raw := range patterns {
		p, err := ParseRepoPattern(raw)
		if err != nil {
			return RepoAllowlist{}, err
		}
		out.patterns = append(out.patterns, p)
	}
	return out, nil
}

// Empty reports an allowlist that admits nothing.
func (a RepoAllowlist) Empty() bool { return len(a.patterns) == 0 }

// Patterns returns the canonical patterns in the order given.
func (a RepoAllowlist) Patterns() []string { return append([]string(nil), a.patterns...) }

func (a RepoAllowlist) String() string {
	if a.Empty() {
		return "(none)"
	}
	return strings.Join(a.patterns, ", ")
}

// Admits reports whether identity, an Identity, matches a pattern.
func (a RepoAllowlist) Admits(identity string) bool {
	identity = strings.ToLower(identity)
	for _, p := range a.patterns {
		if ok, err := path.Match(p, identity); err == nil && ok {
			return true
		}
	}
	return false
}
