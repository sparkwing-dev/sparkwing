package web

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
)

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
