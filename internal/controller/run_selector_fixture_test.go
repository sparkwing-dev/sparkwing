package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type selectorTransactions struct {
	RunTransactions
	team store.Team
	err  error
}

func (s selectorTransactions) ReadRun(_ context.Context, id string) (store.RunRow, error) {
	return store.RunRow{ID: id, Team: s.team}, s.err
}

func selectorTenant(team store.Team, err error) func(context.Context, store.Team) (RunTransactions, error) {
	return func(_ context.Context, selected store.Team) (RunTransactions, error) {
		if selected != team {
			return nil, store.ErrNotFound
		}
		return selectorTransactions{team: team, err: err}, nil
	}
}
