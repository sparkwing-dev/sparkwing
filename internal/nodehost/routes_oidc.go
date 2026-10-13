package nodehost

import (
	"net/http"
	"time"
)

func (s *Server) serveOIDCToken(w http.ResponseWriter, r *http.Request, g *grant) {
	claim, ctx, done, ok := s.forward(w, r, g)
	if !ok {
		return
	}
	defer done()
	var req struct {
		Audience string `json:"audience"`
	}
	if !decodeBody(r, 4<<10, &req) || req.Audience == "" || len(req.Audience) > 1024 {
		s.writeError(w, http.StatusBadRequest, "bad_request", "the body must name an audience")
		return
	}
	var token struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	status, _, err := claim.call(ctx, http.MethodPost, runPath(g.RunID, "oidc-token"),
		map[string]string{"audience": req.Audience}, &token)
	switch {
	case err != nil:
		s.upstreamFailed(w, g, "oidc-token", status, err)
		return
	// safety: the controller refuses a claim it will not sign for with 404 or
	// 422, and either way the node gets no token.
	case status == http.StatusNotFound || status == http.StatusUnprocessableEntity:
		s.writeError(w, http.StatusNotFound, "oidc_unavailable", "the controller issues no OIDC tokens to this node")
		return
	case status != http.StatusOK || token.Token == "":
		s.upstreamFailed(w, g, "oidc-token", status, nil)
		return
	}
	// safety: the token is a credential, so it is masked before the node can print it.
	g.Masker.Register(token.Token)
	s.writeJSON(w, http.StatusOK, map[string]any{"token": token.Token, "expires_at": token.ExpiresAt})
}
