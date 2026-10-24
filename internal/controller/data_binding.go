package controller

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type boundData struct {
	controller *DataController
	authorized capability.Authorized
	authority  blob.Authority
}

func (s *DataController) dataTransactions(ctx context.Context, team string) (dataTransactions, error) {
	return scopedTenantPort[dataTransactions](ctx, store.Team(team), s.ResolveTenant)
}

func (s *boundData) live(ctx context.Context, supplied blob.Authority) (blob.Authority, error) {
	if supplied != s.authority {
		return blob.Authority{}, dataForbidden()
	}
	authority, err := s.controller.DataAuthority(ctx, s.authorized)
	if err != nil {
		return blob.Authority{}, err
	}
	if authority != s.authority {
		return blob.Authority{}, dataForbidden()
	}
	return authority, nil
}

func (s *boundData) authorize(ctx context.Context, operation capability.DataOperation, authority blob.Authority, target capability.DataTarget) error {
	target.Operation = operation
	if scope := s.authorized.Principal.Capability.Data; scope != nil {
		target.RunID, target.NodeID, target.Attempt, target.Generation = scope.RunID, scope.NodeID, scope.Attempt, scope.Generation
	}
	if authority.Attempt.Set {
		attempt := authority.Attempt.Value
		target.RunID, target.NodeID, target.Attempt, target.Generation = attempt.RunID, attempt.NodeID, attempt.Attempt, attempt.Generation
	}
	err := capability.AuthorizeData(ctx, s.controller.Access, s.authorized, target)
	if errors.Is(err, blob.ErrDigestNotReadable) {
		return &apierr.Error{Code: apierr.Forbidden, Message: "digest is not readable", Details: map[string]any{"reason": apierr.ReasonDigestNotReadable}}
	}
	return err
}

func dataProjectionError() error { return errors.New("invalid persisted data projection") }
