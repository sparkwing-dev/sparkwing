package store

import (
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

// cutRepoLength parses the "<len>:" prefix of a scoped key. Every
// digit is checked by hand because a bare pipeline name may itself
// contain ":", and strconv alone would accept sign characters no
// scoped key ever starts with.
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
