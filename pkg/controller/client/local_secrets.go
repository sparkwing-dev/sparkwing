package client

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/secretname"
)

// ResolveLocalSecrets resolves declared requester inputs without exposing provider failures.
func ResolveLocalSecrets(ctx context.Context, declarations []nodeproto.SecretDeclaration, lookup func(context.Context, string) (string, bool, error)) (map[string]string, error) {
	seen := map[string]bool{}
	for _, declaration := range declarations {
		if secretname.Validate(declaration.Name) != nil || seen[declaration.Name] {
			return nil, &apierr.Error{Code: apierr.BadRequest, Message: "invalid or repeated local secret declaration"}
		}
		seen[declaration.Name] = true
	}
	if lookup == nil {
		return nil, &apierr.Error{Code: apierr.Unavailable, Message: "local secret source is unavailable"}
	}
	values := make(map[string]string, len(declarations))
	for _, declaration := range declarations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, present, err := lookup(ctx, declaration.Name)
		if err != nil {
			return nil, &apierr.Error{Code: apierr.Unavailable, Message: "requester local secret resolution failed"}
		}
		if !present {
			if declaration.Optional {
				continue
			}
			return nil, &apierr.Error{Code: apierr.PlanInvalid, Message: "required requester local secret is unavailable", Details: map[string]any{"name": declaration.Name}}
		}
		values[declaration.Name] = value
	}
	return values, nil
}
