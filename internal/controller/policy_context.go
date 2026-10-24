package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func controllerAuthorization(a routepolicy.Authorization, resolve func(context.Context, store.Team) (TenantHandle, error)) routepolicy.Authorization {
	a.Context = func(ctx context.Context) context.Context {
		return context.WithValue(ctx, requestTenantKey{}, &requestTenant{resolve: resolve})
	}
	a.NoteRoute = func(ctx context.Context, pattern string) {
		if record, ok := ctx.Value(auditCtxKey{}).(*auditRecord); ok {
			record.route = pattern
		}
	}
	a.NotePrincipal = notePolicyPrincipal
	a.DataError = dataRouteError
	return a
}
