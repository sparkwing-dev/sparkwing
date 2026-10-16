package capability_test

import (
	"fmt"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRepositoryScopeNarrowingProperty(t *testing.T) {
	invariant := func(owner, name string) bool {
		repository := capability.RepositoryIdentity{Key: "gh:" + name, Display: "https://github.com/" + owner + "/" + name}
		scopes := [][]string{nil, {}, {repository.Key}, {"github.com/" + owner + "/*"}, {"github.com/" + owner + "/" + name}, {"github.com/other/*"}}
		for _, parent := range scopes {
			for _, child := range scopes {
				if capability.RepositoryScopeNarrows(parent, child) && capability.RepositoryAllowed(child, repository) && !capability.RepositoryAllowed(parent, repository) {
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
			if capability.RepositoryAllowed([]string{"github.com/acme/*"}, capability.RepositoryIdentity{Key: "gh:42", Display: display}) {
				t.Fatalf("pattern authorized %q", display)
			}
		}
	}
	if !capability.RepositoryAllowed([]string{"github.com/acme/*"}, capability.RepositoryIdentity{Key: "gh:42", Display: "https://github.com/acme/repo.git"}) {
		t.Fatal("pattern refused repository URL")
	}
}
