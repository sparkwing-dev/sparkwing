package logs

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func pathRunID(r *http.Request) string  { return r.PathValue("runID") }
func queryRunID(r *http.Request) string { return r.URL.Query().Get("run_id") }

type runAccessKey struct{ credential, runID string }

// readableRun lets a request through only when the controller says the caller
// may read the run's logs. The logs service holds every team's logs under
// bare run ids and knows nothing of teams, so the controller, which 404s
// another team's run, decides. Only a yes is cached, for the whoami TTL, so a
// run the caller loses access to stops being readable within that window.
//
// Admin is not exempt from reads, because the dashboard's own credential must
// not become a way around a user's team. Admin and logs.delete are exempt
// from DELETE, which is the operator's retention path and the controller's
// team-deletion path over every team's runs.
func (s *Server) readableRun(runID func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := logsPrincipalFromContext(r.Context())
		if s.authDisabled() || !ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodDelete && (p.hasScope(scopeAdmin) || p.hasScope(scopeLogsDelete)) {
			next.ServeHTTP(w, r)
			return
		}
		id := runID(r)
		if id == "" {
			next.ServeHTTP(w, r)
			return
		}
		key := runAccessKey{credential: p.credential, runID: id}
		if v, ok := s.runAccess.Load(key); ok && time.Now().Before(v.(time.Time)) {
			next.ServeHTTP(w, r)
			return
		}
		status, err := s.controllerReadsRun(r.Context(), p.credential, id)
		if err != nil {
			writeLogsErr(w, status, err.Error())
			return
		}
		if s.authCacheTTL > 0 {
			s.runAccess.Store(key, time.Now().Add(s.authCacheTTL))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) controllerReadsRun(ctx context.Context, credential, runID string) (int, error) {
	u := strings.TrimRight(s.controllerURL, "/") + "/api/v1/runs/" + url.PathEscape(runID) + "/log-access"
	// #nosec G704 -- the origin is operator configuration; the run id is an escaped path segment
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return http.StatusBadGateway, err
	}
	req.Header.Set("Authorization", credential)
	// #nosec G704 -- the request keeps the operator-configured origin
	resp, err := s.authHTTP.Do(req)
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("check run access: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return 0, nil
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return http.StatusNotFound, fmt.Errorf("run %s not found", runID)
	case http.StatusUnauthorized, http.StatusForbidden:
		return resp.StatusCode, fmt.Errorf("the controller refused this caller the run: %s", http.StatusText(resp.StatusCode))
	}
	return http.StatusBadGateway, fmt.Errorf("check run access: controller answered %d", resp.StatusCode)
}
