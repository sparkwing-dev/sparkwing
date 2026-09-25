package store_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A team owner's list of extra repositories is per source repository and per
// team, of the source's owner, at most MaxGitHubAppExtraRepos, and replacing
// it with an empty list clears it.
func TestGitHubAppExtraReposAreAPerSourceListOfTheTeams(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	now := time.Now()

	if got, err := acme.GitHubAppExtraRepos(ctx, "acme/app"); err != nil || len(got) != 0 {
		t.Fatalf("before any list = %v, %v; want none", got, err)
	}
	got, err := acme.SetGitHubAppExtraRepos(ctx, "Acme/App", []string{"acme/proto", "Acme/Lib", "acme/lib", "acme/app"}, "owner", now)
	if want := []string{"acme/lib", "acme/proto"}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("set = %v, %v; want %v, folded, sorted and without the source", got, err, want)
	}
	if got, err := acme.GitHubAppExtraRepos(ctx, "acme/app"); err != nil || len(got) != 2 {
		t.Fatalf("read back = %v, %v", got, err)
	}
	if got, err := acme.GitHubAppExtraRepos(ctx, "acme/other"); err != nil || len(got) != 0 {
		t.Fatalf("another source repository = %v, %v; want none", got, err)
	}
	if got, err := other.GitHubAppExtraRepos(ctx, "acme/app"); err != nil || len(got) != 0 {
		t.Fatalf("another team reads acme's list = %v, %v", got, err)
	}
	all, err := acme.AllGitHubAppExtraRepos(ctx)
	if err != nil || len(all) != 1 || len(all["acme/app"]) != 2 {
		t.Fatalf("all = %v, %v", all, err)
	}
	if _, err := acme.SetGitHubAppExtraRepos(ctx, "acme/app", []string{"evil/secrets"}, "owner", now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("a repository of another owner = %v, want ErrInvalidInput", err)
	}
	var eleven []string
	for i := range store.MaxGitHubAppExtraRepos + 1 {
		eleven = append(eleven, "acme/lib"+strconv.Itoa(i))
	}
	if _, err := acme.SetGitHubAppExtraRepos(ctx, "acme/app", eleven, "owner", now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("%d repositories = %v, want ErrInvalidInput", len(eleven), err)
	}
	if _, err := acme.SetGitHubAppExtraRepos(ctx, "acme/app", eleven[:store.MaxGitHubAppExtraRepos], "owner", now); err != nil {
		t.Fatalf("control: exactly %d repositories = %v", store.MaxGitHubAppExtraRepos, err)
	}
	if _, err := acme.SetGitHubAppExtraRepos(ctx, "acme/app", nil, "owner", now); err != nil {
		t.Fatal(err)
	}
	if got, err := acme.GitHubAppExtraRepos(ctx, "acme/app"); err != nil || len(got) != 0 {
		t.Fatalf("after clearing = %v, %v; want none", got, err)
	}
}
