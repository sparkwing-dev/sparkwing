package nodehost

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
)

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
	value, masked, err := g.Resolver.Resolve(r.Context(), name)
	switch {
	case errors.Is(err, secretsource.ErrMissing):
		s.writeError(w, http.StatusNotFound, "secret_not_found", fmt.Sprintf("secret %q is not set", name))
		return
	case err != nil:
		// safety: a backend error can quote what the backend returned, so it is
		// masked before it reaches the log or the reply.
		detail := g.Masker.Mask(err.Error())
		s.logger.Warn("node secret read failed", "run", g.RunID, "node", g.NodeID, "secret", name, "err", detail)
		s.writeError(w, http.StatusBadGateway, "secret_unavailable", detail)
		return
	}
	if masked {
		g.Masker.Register(value)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"value": value, "masked": masked})
}
