package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// An extra repository carries the GitHub ID recorded when an owner approved
// it, and 0 until one is.
func TestGitHubAppExtraRepoRefs_CarryTheRecordedIDs(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	acme := teamHandle(t, st, "acme")
	if _, err := acme.SetGitHubAppExtraRepos(ctx, "acme/app", []string{"acme/lib", "acme/proto"}, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := acme.SetGitHubAppExtraRepoIDs(ctx, "Acme/App", map[string]int64{"Acme/Lib": 42}); err != nil {
		t.Fatal(err)
	}
	refs, err := acme.GitHubAppExtraRepoRefs(ctx, "acme/app")
	if err != nil || len(refs) != 2 || refs[0].Slug() != "acme/lib" || refs[0].ID != 42 || refs[1].ID != 0 {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
}
