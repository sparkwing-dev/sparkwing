package capability

import (
	"fmt"
	"strings"
	"testing"
	"testing/quick"
)

func TestRepositoryScopeNarrowingProperty(t *testing.T) {
	invariant := func(owner, name string) bool {
		repository := RepositoryIdentity{Key: "gh:" + name, Display: "https://github.com/" + owner + "/" + name}
		scopes := [][]string{nil, {}, {repository.Key}, {"github.com/" + owner + "/*"}, {"github.com/" + owner + "/" + name}, {"github.com/other/*"}}
		for _, parent := range scopes {
			for _, child := range scopes {
				if repositoryScopeNarrows(parent, child) && RepositoryAllowed(child, repository) && !RepositoryAllowed(parent, repository) {
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(invariant, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPatternsKeepHostAndOwnerBoundaries(t *testing.T) {
	for i := range 40 {
		for _, display := range []string{
			fmt.Sprintf("https://github.com/acme-evil/repo%d", i),
			fmt.Sprintf("https://github.com.evil/acme/repo%d", i),
			fmt.Sprintf("https://github.com/acme/repo%d/extra", i),
			fmt.Sprintf("https://github.com/acme@evil/repo%d", i),
		} {
			if RepositoryAllowed([]string{"github.com/acme/*"}, RepositoryIdentity{Key: "gh:42", Display: display}) {
				t.Fatalf("pattern authorized %q", display)
			}
		}
	}
	if !RepositoryAllowed([]string{"github.com/acme/*"}, RepositoryIdentity{Key: "gh:42", Display: "https://github.com/acme/repo.git"}) {
		t.Fatal("pattern refused repository URL")
	}
}

// safety: a bare owner/name display is read as github.com only for an App-verified gh:<id> key.
func FuzzRepositorySlugDisplayStaysOnItsOwner(f *testing.F) {
	for _, seed := range []string{"acme/widgets", "acme-evil/widgets", "acme/my.repo", "evil.com/acme", "acme/a/b", "x:y/acme", "acme@evil/widgets"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, display string) {
		owner, name, _ := strings.Cut(display, "/")
		app := RepositoryAllowed([]string{"github.com/acme/*"}, RepositoryIdentity{Key: "gh:42", Display: display})
		if app && (!strings.EqualFold(owner, "acme") || name == "" || strings.ContainsAny(name, "/:")) {
			t.Fatalf("App slug %q escaped its owner", display)
		}
		if RepositoryAllowed([]string{"github.com/acme/*"}, RepositoryIdentity{Key: "git:example.com/acme/widgets", Display: display}) && !strings.Contains(display, "github.com") {
			t.Fatalf("non-App identity %q was read as github.com", display)
		}
	})
	if !RepositoryAllowed([]string{"github.com/acme/*"}, RepositoryIdentity{Key: "gh:42", Display: "acme/widgets"}) {
		f.Fatal("App slug display refused by its owner's pattern")
	}
}
