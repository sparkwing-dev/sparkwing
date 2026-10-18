package capability

import (
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// RepositoryPattern selects a repository by its display name.
type RepositoryPattern string

// RepositoryIdentity contains the canonical identity and trusted display name.
type RepositoryIdentity struct {
	Key     string
	Display string
}

// RepositoryAllowed evaluates nil as unrestricted and an empty list as no authority.
func RepositoryAllowed(scope []string, repository RepositoryIdentity) bool {
	if scope == nil {
		return true
	}
	display := repository.Display
	// safety: an App-verified gh:<id> key names a GitHub repository, so its owner/name display lives on github.com.
	if owner, _, _ := strings.Cut(display, "/"); strings.HasPrefix(repository.Key, "gh:") && strings.Count(display, "/") == 1 && !strings.ContainsAny(owner, ".:") && !strings.Contains(display, ":") {
		display = "github.com/" + display
	}
	for _, entry := range scope {
		if entry == repository.Key && repository.Key != "" || repositoryPatternMatches(RepositoryPattern(entry), display) {
			return true
		}
	}
	return false
}

func repositoryPatternMatches(pattern RepositoryPattern, display string) bool {
	allow, err := sourceurl.ParseRepoAllowlist([]string{string(pattern)})
	if err != nil {
		return false
	}
	if display != "" && !strings.Contains(display, ":") {
		display = "https://" + display
	}
	named, admitted := allow.AdmitsRepository(display, "", "", "")
	return named && admitted
}

// RepositoryScopeNarrows rejects unrestricted children of restricted parents.
func RepositoryScopeNarrows(parent, child []string) bool {
	if parent == nil {
		return true
	}
	if child == nil {
		return false
	}
	for _, entry := range child {
		allowed := false
		for _, limit := range parent {
			if entry == limit || !strings.Contains(entry, "*") && RepositoryAllowed([]string{limit}, RepositoryIdentity{Key: entry, Display: entry}) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}
