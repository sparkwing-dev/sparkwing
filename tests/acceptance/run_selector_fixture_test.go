package acceptance_test

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type selectorTransactions struct {
	controller.RunTransactions
	team store.Team
	err  error
}

func (s selectorTransactions) ResolveRunID(_ context.Context, id string) (string, error) {
	return id, s.err
}

func selectorTenant(team store.Team, err error) func(context.Context, store.Team) (controller.TenantHandle, error) {
	return func(_ context.Context, selected store.Team) (controller.TenantHandle, error) {
		if selected != team {
			return nil, store.ErrNotFound
		}
		return selectorTransactions{team: team, err: err}, nil
	}
}

func (s selectorTransactions) Team() store.Team { return s.team }
