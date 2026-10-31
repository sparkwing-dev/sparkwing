package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestCacheDefaultLookupRequiresProvenRepository(t *testing.T) {
	st, ctx := openTestStore(t)
	referenceWorld(t, st, "gh:701")
	input := runspec.Request{Pipeline: "ci", Git: runspec.Git{Repository: runspec.Repository{Key: "gh:701"}, Ref: "refs/heads/feature", SHA: referenceFeature}, Source: runspec.Source{Ref: referenceFeature}}
	for _, proven := range []bool{false, true} {
		request := IntakeRequest{}
		if proven {
			request.FetchedRepository = FetchedRepository{ID: 701, Name: "acme/app"}
		}
		err := st.inTx(ctx, func(tx *storeTx) error {
			p, err := sealProvenanceTx(ctx, tx, "a", request, capability.Principal{Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}}}, input, "run")
			if err != nil {
				return err
			}
			if (p.DefaultBranch() == "main") != proven {
				t.Fatalf("proven=%v acquired default branch %q", proven, p.DefaultBranch())
			}
			sealed := runspec.SealedRequest{Request: input, Provenance: p}
			if proven {
				sealed.Request.Git.Repository.Key = "gh:999"
			}
			scope, err := awardDataScope(awardRow{repo: "gh:999"}, sealed, capability.Capability{}, "")
			if err != nil {
				return err
			}
			if scope.RepositoryKey != "gh:701" {
				t.Fatalf("proven=%v used asserted repository: %+v", proven, scope)
			}
			for _, ref := range scope.ReadRefs {
				if !proven && ref == "refs/heads/main" {
					t.Fatal("unproven run received repository cache fallback")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
