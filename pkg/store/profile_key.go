package store

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// JoinProfileKey builds the identity a pipeline's capacity profile is
// stored under: the repository identity length-prefixed ahead of the
// pipeline name, because both halves can carry "/" (canonical repo
// identities are host/owner/path) and a separator alone cannot keep
// them apart. A run outside any repository keeps the bare pipeline
// name.
func JoinProfileKey(repo, pipeline string) string {
	if repo == "" || pipeline == "" {
		return pipeline
	}
	return strconv.Itoa(len(repo)) + ":" + repo + pipeline
}

// SplitProfileKey recovers the repo and pipeline halves of a stored
// profile key. Keys written before v0.37.2 were "repo/pipeline" with
// neither half length-prefixed; those split on the last "/", the rule
// that wrote them, so rows already in a store keep resolving. A key
// with no scope at all is a bare pipeline name.
func SplitProfileKey(key string) (repo, pipeline string) {
	if n, rest, ok := cutRepoLength(key); ok {
		return rest[:n], rest[n:]
	}
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

// DisplayProfileKey renders a stored profile key for humans: repo
// scope and pipeline joined with "/", the shape tables and warnings
// always showed, instead of the stored length-prefixed encoding. The
// rendered form is accepted back by the CLI's reset and filter
// matching, so a key copied from output stays actionable.
func DisplayProfileKey(key string) string {
	repo, pipeline := SplitProfileKey(key)
	if repo == "" {
		return pipeline
	}
	return repo + "/" + pipeline
}

func cutRepoLength(key string) (int, string, bool) {
	digits, rest, ok := strings.Cut(key, ":")
	if !ok || digits == "" {
		return 0, "", false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, "", false
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 || n >= len(rest) {
		return 0, "", false
	}
	return n, rest, true
}

// RepoIdentityFromURL derives the repository identity a profile key is
// scoped by from a git remote URL: the lowercased host joined to the
// path, with any credentials and ".git" suffix removed. A filesystem
// remote has no host to name it by, so it hashes to a stable
// "local:" identity instead. An unparseable remote has no identity.
func RepoIdentityFromURL(remote string) string {
	remote = strings.TrimSpace(remote)
	if strings.HasPrefix(remote, "/") || (len(remote) >= 3 && remote[1] == ':' && (remote[2] == '/' || remote[2] == '\\')) {
		return RepoIdentityFromPath(remote)
	}
	if !strings.Contains(remote, "://") {
		if colon := strings.Index(remote, ":"); colon > 0 {
			remote = "ssh://" + remote[:colon] + "/" + remote[colon+1:]
		}
	}
	parsed, err := url.Parse(remote)
	if err != nil {
		return ""
	}
	if parsed.Scheme == "file" {
		return RepoIdentityFromPath(parsed.Host + parsed.Path)
	}
	if parsed.Host == "" {
		return ""
	}
	host := parsed.Host
	if parsed.User != nil {
		host = strings.TrimPrefix(host, parsed.User.String()+"@")
	}
	p := strings.TrimSuffix(strings.Trim(strings.TrimSpace(parsed.Path), "/"), ".git")
	if p == "" {
		return ""
	}
	return strings.ToLower(host) + "/" + p
}

// RepoIdentityFromPath names a checkout that has no remote to be
// identified by, hashing its cleaned path so two checkouts of the same
// directory agree and two different directories do not.
func RepoIdentityFromPath(repoPath string) string {
	normalized := path.Clean(strings.ReplaceAll(strings.TrimSpace(repoPath), "\\", "/"))
	normalized = strings.TrimSuffix(normalized, ".git")
	if normalized == "" || normalized == "." {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("local:%x", sum[:12])
}

// RepoIdentityMatches reports whether a profile key's repository scope
// names the same repository as a run or trigger row. The key carries the
// canonical identity ("host/owner/name"); a row records the slug it was
// triggered for ("owner/name") and, when it has one, the clone URL the
// identity derives from. A clone URL decides alone when the row has one,
// so a slug cannot claim a key under a host the URL contradicts. Without
// a URL the slug matches only under a bare host, so a caller cannot reach
// "host/other/owner/name" by naming its own "owner/name". A row with no
// repository at all matches no scoped key.
func RepoIdentityMatches(keyRepo, repo, repoURL string) bool {
	if keyRepo == "" {
		return true
	}
	// safety: a clone URL names the repository exactly, so it decides alone; the slug rule is only for rows without one.
	if identity := RepoIdentityFromURL(repoURL); identity != "" {
		return keyRepo == identity
	}
	if repo == "" {
		return false
	}
	if keyRepo == repo {
		return true
	}
	host, ok := strings.CutSuffix(keyRepo, "/"+repo)
	return ok && host != "" && !strings.Contains(host, "/")
}
