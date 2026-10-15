package nodehost

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func (g *grant) oidc(ctx context.Context, audience string) (ProcessOIDCToken, error) {
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	if len(audience) > 1024 {
		return ProcessOIDCToken{}, executorError(apierr.BadRequest, "invalid OIDC audience")
	}
	if g.Claim == nil {
		return ProcessOIDCToken{}, serviceUnavailable()
	}
	var token ProcessOIDCToken
	status, _, err := g.Claim.call(ctx, http.MethodPost, runPath(g.RunID, "oidc-token"), map[string]string{"audience": audience}, &token)
	if err != nil || status != http.StatusOK {
		return ProcessOIDCToken{}, serviceUnavailable()
	}
	return token, nil
}
