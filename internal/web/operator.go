package web

import (
	"net/http"
	"strings"
	"time"
)

// safety: the console's page carries no data of its own, but only the
// operator's session is served it, so its shape is not handed to every
// signed-in account; the controller still authorizes every call it makes.
func operatorPageGate(opts HandlerOptions, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		console := p == "/operator" || strings.HasPrefix(p, "/operator/") || strings.HasPrefix(p, "/operator.")
		if console && !operatorSession(r, opts.ControllerURL) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "the operator console needs the operator's own signed-in account", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func operatorSession(r *http.Request, controllerURL string) bool {
	id := sessionIDFromContext(r.Context())
	if id == "" || controllerURL == "" {
		return false
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		strings.TrimRight(controllerURL, "/")+"/api/v1/operator/session", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", sessionAuthorization(id))
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
