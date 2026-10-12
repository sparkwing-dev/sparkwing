package nodehost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
)

// safety: a stalled backend must not hold a node's request open.
const resolveTimeout = 10 * time.Second

func (s *Server) serveSecret(w http.ResponseWriter, r *http.Request, g *grant) {
	name := r.PathValue("name")
	if !g.declared[name] {
		// safety: a read of a name the pipeline never declared is a probe, so
		// it is audited and refused without reaching the secret source.
		s.logger.Warn("audit", "event", "secret_undeclared", "run", g.RunID, "node", g.NodeID, "secret", name)
		s.writeError(w, http.StatusForbidden, "secret_undeclared",
			fmt.Sprintf("node %q read secret %q, which its pipeline does not declare; declare it in the pipeline's secrets", g.NodeID, name))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), resolveTimeout)
	defer cancel()
	//nolint:contextcheck // revoking the grant must end the read, not just the request's own context.
	defer context.AfterFunc(g.ctx, cancel)()
	value, masked, err := g.Resolver.Resolve(ctx, name)
	switch {
	case errors.Is(err, secretsource.ErrMissing):
		s.writeError(w, http.StatusNotFound, "secret_not_found", fmt.Sprintf("secret %q is not set", name))
		return
	case err != nil:
		// safety: a backend error can quote backend URLs and replies, so only its type is logged.
		s.logger.Warn("node secret read failed", "run", g.RunID, "node", g.NodeID, "secret", name,
			"err_type", fmt.Sprintf("%T", err))
		s.writeError(w, http.StatusBadGateway, "secret_unavailable", fmt.Sprintf("secret %q could not be read", name))
		return
	}
	if masked {
		g.Masker.Register(value)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"value": value, "masked": masked})
}
