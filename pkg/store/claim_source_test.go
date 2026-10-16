package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestGitHubAppExtraRepoRefs_CarryTheRecordedIDs(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	acme := teamHandle(t, st, "acme")
	if _, err := acme.BindGitHubAppInstallation(ctx, acmeInstallation(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.SetAuthorizedGitHubExtraRepos(ctx, githubAppAuthorization(t, st, acme), "acme/app", []string{"acme/lib", "acme/proto"}, map[string]int64{"acme/lib": 42, "acme/proto": 43}); err != nil {
		t.Fatal(err)
	}
	refs, err := acme.GitHubAppExtraRepoRefs(ctx, "acme/app")
	if err != nil || len(refs) != 2 || refs[0].Slug() != "acme/lib" || refs[0].ID != 42 || refs[1].ID != 43 {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
}
