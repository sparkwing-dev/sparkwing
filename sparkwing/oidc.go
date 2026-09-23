package sparkwing

import (
	"context"
	"errors"
)

// ErrOIDCUnavailable reports that the node runs where no controller can
// sign an ID token for it, such as a local run.
var ErrOIDCUnavailable = errors.New("sparkwing: no controller issues OIDC tokens for this run")

type oidcTokenSourceKey struct{}

// OIDCTokenSource signs an ID token for the current run with the given
// audience. The runtime installs one on the node's context.
type OIDCTokenSource func(ctx context.Context, audience string) (string, error)

// OIDCToken returns an ID token the controller signed for this run, with
// aud set to audience. Exchange it with a cloud provider for short-lived
// credentials, so the pipeline stores no cloud key:
//
//	token, err := sparkwing.OIDCToken(ctx, "sts.amazonaws.com")
//
// The token names the run's team, pipeline, trigger, runner kind and ref in
// its sub claim; see docs/oidc.md for the format and provider setup. It
// returns ErrOIDCUnavailable when the node runs with no controller behind
// it. The runtime masks every token it returns in logs.
func OIDCToken(ctx context.Context, audience string) (string, error) {
	src, ok := ctx.Value(oidcTokenSourceKey{}).(OIDCTokenSource)
	if !ok || src == nil {
		return "", ErrOIDCUnavailable
	}
	return src(ctx, audience)
}
