package web

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
)

// CapabilitiesHandler serves GET /api/v1/capabilities from the
// dashboard's Backend so every topology answers the same way.
func CapabilitiesHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caps, err := b.Capabilities(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, caps)
	}
}
