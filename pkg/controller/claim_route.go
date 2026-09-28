package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a claim principal carries no scopes, so no scope-gated route admits it.
const principalKindClaim = "claim"

// safety: a claim token reaching a route not wrapped in this is refused as an
// unknown bearer, so every route stays closed to claim tokens until it opts in.
type claimTokenRoute struct {
	class store.ClaimRouteClass
	next  http.Handler
}

func newClaimTokenRoute(class store.ClaimRouteClass, next http.Handler) *claimTokenRoute {
	return &claimTokenRoute{class: class, next: next}
}

// A path's run, and for reporting and result routes its node, must be the
// token's own; the mux fills the path only once it routes here.
func (c *claimTokenRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok, ok := claimTokenFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:    "claim_token_required",
			Message: "this route answers only a claim token",
		})
		return
	}
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	ownNode := c.class == store.ClaimReporting || c.class == store.ClaimResult
	if (runID != "" && runID != tok.RunID) || (ownNode && nodeID != tok.NodeID) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:    "claim_mismatch",
			Message: "this claim token is bound to another run or node",
		})
		return
	}
	c.next.ServeHTTP(w, r)
}

type claimTokenCtxKey struct{}

func claimTokenFromContext(ctx context.Context) (store.ClaimToken, bool) {
	tok, ok := ctx.Value(claimTokenCtxKey{}).(store.ClaimToken)
	return tok, ok
}

func claimRouteFor(mux *http.ServeMux, r *http.Request) (string, *claimTokenRoute) {
	raw, err := extractBearer(r)
	if err != nil || !strings.HasPrefix(raw, store.ClaimTokenPrefix+"_") {
		return "", nil
	}
	h, _ := mux.Handler(r)
	route, ok := h.(*claimTokenRoute)
	if !ok {
		return "", nil
	}
	return raw, route
}

// safety: this never consults the token cache; every request reads the claim
// row, which is what refuses a lost or cancelled claim at once.
func (s *Server) serveClaim(w http.ResponseWriter, r *http.Request, raw string, route *claimTokenRoute, next http.Handler) {
	tok, err := s.store.AuthorizeClaimToken(r.Context(), raw, route.class, time.Now())
	if err != nil {
		writeClaimRefusal(w, r, s, err)
		return
	}
	p := &Principal{
		Name:        tok.RunID + "/" + tok.NodeID,
		Kind:        principalKindClaim,
		TokenPrefix: tok.Prefix,
		Authed:      time.Now().UTC(),
		Team:        tok.Team,
		Expires:     tok.ExpiresAt,
	}
	observeRequestPrincipal(p.Kind)
	ctx := context.WithValue(contextWithPrincipal(r.Context(), p), claimTokenCtxKey{}, tok)
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Principal: p.Name})
	next.ServeHTTP(w, r.WithContext(ctx))
}

func writeClaimRefusal(w http.ResponseWriter, r *http.Request, s *Server, err error) {
	switch {
	case errors.Is(err, store.ErrClaimTokenInvalid):
		writeAuthError(w, http.StatusUnauthorized, authErrorBody{Code: "unauthenticated", Message: err.Error()})
	case errors.Is(err, store.ErrClaimNotLive):
		writeAuthError(w, http.StatusForbidden, authErrorBody{Code: "claim_ended", Message: err.Error()})
	case errors.Is(err, store.ErrClaimCancelRequested):
		writeAuthError(w, http.StatusForbidden, authErrorBody{Code: "claim_cancelled", Message: err.Error()})
	default:
		s.writeInternalError(w, r, "claim token", err)
	}
}
