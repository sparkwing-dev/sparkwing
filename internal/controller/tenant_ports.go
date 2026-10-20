package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// TenantHandle is the team identity of a request-scoped store view.
type (
	TenantHandle     interface{ Team() store.Team }
	requestTenantKey struct{}
	requestTenant    struct {
		handle  TenantHandle
		resolve func(context.Context, store.Team) (TenantHandle, error)
	}
)

func (s *Server) requestTenant(ctx context.Context, team store.Team) (TenantHandle, error) {
	return scopedTenant(ctx, team, s.resolveTenant)
}

func scopedTenant(ctx context.Context, team store.Team, resolve func(context.Context, store.Team) (TenantHandle, error)) (TenantHandle, error) {
	if team == "" {
		return nil, missingEndpointPolicy()
	}
	if scope, ok := ctx.Value(requestTenantKey{}).(*requestTenant); ok && scope.handle != nil {
		if scope.handle.Team() != team {
			return nil, missingEndpointPolicy()
		}
		return scope.handle, nil
	}
	if scope, ok := ctx.Value(requestTenantKey{}).(*requestTenant); ok && scope.resolve != nil {
		resolve = scope.resolve
	}
	if resolve == nil {
		return nil, missingEndpointPolicy()
	}
	handle, err := resolve(ctx, team)
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		err = store.ErrNotFound
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if handle == nil || handle.Team() != team {
		return nil, missingEndpointPolicy()
	}
	if scope, ok := ctx.Value(requestTenantKey{}).(*requestTenant); ok {
		scope.handle = handle
	}
	return handle, nil
}

func tenantPort[P any](ctx context.Context, s *Server, team store.Team) (P, error) {
	var zero P
	handle, err := s.requestTenant(ctx, team)
	if err != nil {
		return zero, err
	}
	port, ok := handle.(P)
	if !ok {
		return zero, missingEndpointPolicy()
	}
	return port, nil
}

// WithTenantResolver supplies the single tenant dependency before Handler is built.
// Every narrow handler port is selected from the returned handle.
func (s *Server) WithTenantResolver(resolve func(context.Context, store.Team) (TenantHandle, error)) *Server {
	s.resolveTenant = resolve
	return s
}

func (s *Server) tenantEndpoint(endpoint Endpoint) Endpoint {
	handle := endpoint.Handle
	endpoint.Handle = func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		if a.Target.Team != "" && s.resolveTenant != nil {
			if _, err := s.requestTenant(r.Context(), store.Team(a.Target.Team)); err != nil {
				return err
			}
		}
		return handle(w, r, a)
	}
	return endpoint
}
