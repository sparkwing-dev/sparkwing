package capability

import (
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// repositoryPattern selects a repository by its display name.
type repositoryPattern string

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
	// safety: intake takes a gh:<id> key's owner/name display from the App delivery or installation record,
	// never from the caller, so the slug names that repository on github.com.
	if owner, _, _ := strings.Cut(display, "/"); strings.HasPrefix(repository.Key, "gh:") && strings.Count(display, "/") == 1 && !strings.ContainsAny(owner, ".:") && !strings.Contains(display, ":") {
		display = "github.com/" + display
	}
	for _, entry := range scope {
		if entry == repository.Key && repository.Key != "" || repositoryPatternMatches(repositoryPattern(entry), display) {
			return true
		}
	}
	return false
}

func repositoryPatternMatches(pattern repositoryPattern, display string) bool {
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

// repositoryScopeNarrows rejects unrestricted children of restricted parents.
func repositoryScopeNarrows(parent, child []string) bool {
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
