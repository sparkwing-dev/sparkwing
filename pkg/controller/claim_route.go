package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a claim principal carries no scopes, so no scope-gated route admits it.
const principalKindClaim = "claim"

// safety: a token whose kind is not in kinds is refused, so an empty list admits none.
type claimRouteSpec struct {
	class store.ClaimRouteClass
	kinds []store.ClaimTokenKind
	// safety: nil reads the path's {id} and {nodeID}, and an empty run is refused,
	// so a route with no run in its path is closed until it binds one itself.
	bind func(*http.Request) (runID, nodeID string)
}

// safety: a claim token reaching a route not wrapped in this is refused as an
// unknown bearer, so every route stays closed to claim tokens until it opts in.
type claimTokenRoute struct {
	spec   claimRouteSpec
	next   http.Handler
	result claimResultHandler
	store  *store.Store
	// safety: serves only requests that carry no claim token, so a path a
	// claim route shares with a scoped route keeps that route for every
	// other bearer and never hands it a claim.
	fallback http.Handler
}

func (c *claimTokenRoute) orElse(h http.Handler) *claimTokenRoute {
	c.fallback = h
	return c
}

// safety: the handler writes only through a store function that takes commit and
// commits the result in the same transaction; an ended claim never reaches it.
type claimResultHandler func(w http.ResponseWriter, r *http.Request, commit store.ClaimResultCommit)

const maxClaimResultBytes = 16 << 20

// safety: authorization precedes the handler, so a sensitive route that writes
// must re-check the claim and its cancel inside the store transaction that
// writes, taking locks in the store's order (lockTeamRunRowTx).
func newClaimSensitiveRoute(kinds []store.ClaimTokenKind, bind func(*http.Request) (string, string),
	next http.Handler,
) *claimTokenRoute {
	return &claimTokenRoute{spec: claimRouteSpec{class: store.ClaimSensitive, kinds: kinds, bind: bind}, next: next}
}

func newClaimReportingRoute(kinds []store.ClaimTokenKind, next http.Handler) *claimTokenRoute {
	return newClaimReportingRouteBound(kinds, nil, next)
}

func newClaimReportingRouteBound(kinds []store.ClaimTokenKind, bind func(*http.Request) (string, string),
	next http.Handler,
) *claimTokenRoute {
	return &claimTokenRoute{spec: claimRouteSpec{class: store.ClaimReporting, kinds: kinds, bind: bind}, next: next}
}

// safety: the body's digest is the result's identity, so an ended claim is
// answered from the digest it committed, 200 when identical and 409 otherwise,
// without calling h.
func (s *Server) newClaimResultRoute(kinds []store.ClaimTokenKind, bind func(*http.Request) (string, string),
	h claimResultHandler,
) *claimTokenRoute {
	return &claimTokenRoute{
		spec:   claimRouteSpec{class: store.ClaimResult, kinds: kinds, bind: bind},
		result: h, store: s.store,
	}
}

func pathClaimBinding(r *http.Request) (string, string) {
	return r.PathValue("id"), r.PathValue("nodeID")
}

// safety: the mux fills the path only once it routes here, so the binding is
// checked here rather than where the token is authenticated.
func (c *claimTokenRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok, ok := claimTokenFromContext(r.Context())
	if !ok && c.fallback != nil {
		c.fallback.ServeHTTP(w, r)
		return
	}
	if !ok {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:    "claim_token_required",
			Message: "this route answers only a claim token",
		})
		return
	}
	if !slices.Contains(c.spec.kinds, tok.Kind) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:    "claim_kind",
			Message: "this route does not answer a " + string(tok.Kind) + " claim token",
		})
		return
	}
	bind := c.spec.bind
	if bind == nil {
		bind = pathClaimBinding
	}
	runID, nodeID := bind(r)
	ownNode := c.spec.class == store.ClaimReporting || c.spec.class == store.ClaimResult
	if runID == "" || runID != tok.RunID || (ownNode && (nodeID == "" || nodeID != tok.NodeID)) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:    "claim_mismatch",
			Message: "this claim token is bound to another run or node",
		})
		return
	}
	if c.spec.class != store.ClaimResult {
		c.next.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxClaimResultBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if !tok.Ended {
		r.Body = io.NopCloser(bytes.NewReader(body))
		c.result(w, r, store.NewClaimResultCommit(tok, digest))
		return
	}
	switch err := c.store.ReplayClaimResult(r.Context(), tok, digest); {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": "replayed"})
	case errors.Is(err, store.ErrClaimResultConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, store.ErrClaimTokenInvalid):
		writeAuthError(w, http.StatusUnauthorized, authErrorBody{Code: "unauthenticated", Message: err.Error()})
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
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
	tok, err := s.store.AuthorizeClaimToken(r.Context(), raw, route.spec.class, time.Now())
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
	stampPrincipal(ctx, p)
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
