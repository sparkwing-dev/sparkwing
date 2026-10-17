package controller_test

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (f *appFixture) trigger(id string) (*store.Trigger, error) {
	f.t.Helper()
	team, err := f.store.AsOperator().RunTeam(context.Background(), id)
	if err != nil {
		return nil, err
	}
	tn, err := f.store.ForTeam(context.Background(), team)
	if err != nil {
		return nil, err
	}
	return tn.GetTrigger(context.Background(), id)
}
