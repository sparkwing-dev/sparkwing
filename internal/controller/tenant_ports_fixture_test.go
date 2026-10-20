package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func testOnlyTenantPort[P any](input interface{ fixtureServer() *Server }, resolve func(context.Context, store.Team) (P, error)) *Server {
	s := input.fixtureServer()
	if resolve == nil {
		return s.WithTenantResolver(nil)
	}
	return s.WithTenantResolver(func(ctx context.Context, team store.Team) (TenantHandle, error) {
		port, err := resolve(ctx, team)
		if any(port) == nil {
			return nil, err
		}
		return any(port).(TenantHandle), err
	})
}
