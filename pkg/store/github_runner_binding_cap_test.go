package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestATeamBindsAtMostTwentyRepositories(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, st, "acme")
	fund(t, acme)
	operator, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	bind := func(tn *store.Tenant, id int64) error {
		_, err := tn.AddGitHubRunnerBinding(ctx, store.GitHubRunnerBinding{
			RepositoryID: id, RepositoryOwnerID: 7, Repository: fmt.Sprintf("acme/repo-%d", id), CreatedBy: "u1",
		}, now)
		return err
	}
	for id := int64(1); id <= store.MaxGitHubRunnerBindings; id++ {
		if err := bind(acme, id); err != nil {
			t.Fatalf("binding %d: %v", id, err)
		}
	}
	if err := bind(acme, 99); !errors.Is(err, store.ErrBindingLimit) {
		t.Fatalf("a funded team's twenty-first binding = %v, want ErrBindingLimit", err)
	}
	for id := int64(1); id <= store.MaxGitHubRunnerBindings+1; id++ {
		if err := bind(operator, 1000+id); err != nil {
			t.Fatalf("the operator's binding %d: %v", id, err)
		}
	}
}
