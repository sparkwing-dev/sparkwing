package acceptance_test

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func bindTenantPort[P any](s *controller.Server, resolve func(context.Context, store.Team) (P, error)) *controller.Server {
	if resolve == nil {
		return s.WithTenantResolver(nil)
	}
	return s.WithTenantResolver(func(ctx context.Context, team store.Team) (controller.TenantHandle, error) {
		port, err := resolve(ctx, team)
		if any(port) == nil {
			return nil, err
		}
		return any(port).(controller.TenantHandle), err
	})
}
