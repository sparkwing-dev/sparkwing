package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: ForTeam reads the team registry on every call, and a runner fleet
// polling run routes would pay that read per request. A team removed inside
// the window keeps a live handle for at most this long; removing a team is an
// operator path that drains its rows first.
const tenantCacheTTL = 30 * time.Second

type tenantCache struct {
	mu      sync.Mutex
	entries map[store.Team]tenantCacheEntry
}

type tenantCacheEntry struct {
	tenant  *store.Tenant
	expires time.Time
}

func (c *tenantCache) get(team store.Team, now time.Time) (*store.Tenant, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[team]
	if !ok || !now.Before(e.expires) {
		return nil, false
	}
	return e.tenant, true
}

func (c *tenantCache) put(team store.Team, t *store.Tenant, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[store.Team]tenantCacheEntry{}
	}
	c.entries[team] = tenantCacheEntry{tenant: t, expires: now.Add(tenantCacheTTL)}
}

// requestTeam is the team a request acts for. A request with no principal is
// the unauthenticated local path, and a principal with no team is the host's
// own peer or a credential minted before tokens carried one; both act for the
// team every such row was migrated into. A signed-in account never falls back,
// because an account that lost its team must reach no team's rows.
func requestTeam(r *http.Request) (store.Team, error) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		return store.DefaultTeam, nil
	}
	if p.Team != "" {
		return p.Team, nil
	}
	if p.AccountID != "" {
		return "", store.ErrNoTeam
	}
	return store.DefaultTeam, nil
}

// tenantFor returns the store handle for the team the caller acts for. Every
// tenant-owned read and write a handler makes goes through it, and the team is
// never taken from the request body, path or query.
func (s *Server) tenantFor(r *http.Request) (*store.Tenant, error) {
	team, err := requestTeam(r)
	if err != nil {
		return nil, err
	}
	return s.tenantForTeam(r.Context(), team)
}

func (s *Server) tenantForTeam(ctx context.Context, team store.Team) (*store.Tenant, error) {
	team = store.NormalizeTeam(team)
	now := time.Now()
	if t, ok := s.tenants.get(team, now); ok {
		return t, nil
	}
	t, err := s.store.ForTeam(ctx, team)
	if err != nil {
		return nil, err
	}
	s.tenants.put(team, t, now)
	return t, nil
}

// requestTenant resolves the caller's tenant or answers the request. A caller
// whose team is missing or unregistered is refused rather than served another
// team's view.
func (s *Server) requestTenant(w http.ResponseWriter, r *http.Request) (*store.Tenant, bool) {
	t, err := s.tenantFor(r)
	if err == nil {
		return t, true
	}
	if errors.Is(err, store.ErrNoTeam) || errors.Is(err, store.ErrUnknownTeam) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:    "no_team",
			Message: "the credential acts for no registered team",
		})
		return nil, false
	}
	s.writeInternalError(w, r, "team handle", err)
	return nil, false
}

// safety: these routes name a run id in their path but are answered for a
// caller other than the run's team, so the boundary leaves them to their own
// gate. Each entry says what that gate is.
var teamBoundaryExempt = map[string]string{
	"POST /api/v1/runs/{id}/nodes/{nodeID}/claim/validate": "the logs service asks whether a runner's " +
		"log-write claim is live; the claim it validates is bound to the claimant's own team",
}

// teamBoundaryRunID reports the run id a request addresses when its route is
// scoped to one run or trigger, and whether the boundary applies.
func teamBoundaryRunID(pattern string, r *http.Request) (string, bool) {
	if _, exempt := teamBoundaryExempt[pattern]; exempt {
		return "", false
	}
	_, path, _ := strings.Cut(pattern, " ")
	var prefix string
	switch {
	case strings.HasPrefix(path, "/api/v1/runs/{id}"):
		prefix = "/api/v1/runs/"
	case strings.HasPrefix(path, "/api/v1/triggers/{id}"):
		prefix = "/api/v1/triggers/"
	default:
		return "", false
	}
	rest := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
	seg, _, _ := strings.Cut(rest, "/")
	id, err := url.PathUnescape(seg)
	if err != nil || id == "" {
		return "", false
	}
	return id, true
}

// teamBoundary answers 404 for any route scoped to one run or trigger whose
// row belongs to another team than the caller's, before the route's own
// handler runs. It sits in front of the mux rather than on each route so a
// route added under /api/v1/runs/{id} is inside the boundary without anyone
// remembering to put it there. The answer is the one a missing run gets, so a
// caller cannot tell another team's run from no run.
func (s *Server) teamBoundary(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		runID, scoped := teamBoundaryRunID(pattern, r)
		if !scoped {
			next.ServeHTTP(w, r)
			return
		}
		t, ok := s.requestTenant(w, r)
		if !ok {
			return
		}
		owned, err := t.OwnsRun(r.Context(), runID)
		if err != nil {
			s.writeInternalError(w, r, "team boundary", err)
			return
		}
		if !owned {
			writeError(w, http.StatusNotFound, runNotFound(runID))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func runNotFound(runID string) error {
	return fmt.Errorf("run %q: %w", runID, store.ErrNotFound)
}

// writeClaimTeamRefusal answers a claim whose credential names no team. The
// claim is the caller's to fix, by using a token minted for a team, so it is
// a 403 rather than a server fault.
func writeClaimTeamRefusal(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, store.ErrClaimantHasNoTeam) {
		return false
	}
	writeAuthError(w, http.StatusForbidden, authErrorBody{
		Code:    "claim_no_team",
		Message: "this credential carries no team, so it can claim no work; use a token minted for a team",
	})
	return true
}

// handleRunLogAccess answers the logs service, which stores every team's logs
// under bare run ids, whether the caller may read one run's logs. The team
// boundary in front of the mux has already answered 404 for another team's
// run, so reaching here is the yes.
func handleRunLogAccess(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// runTeam answers the team of the run a request names, and refuses unless it
// is the caller's own. The team boundary has already answered another team's
// run 404; this repeats the check because a grant it feeds opens that team's
// namespace in the shared cache.
func (s *Server) runTeam(r *http.Request) (store.Team, error) {
	t, err := s.tenantFor(r)
	if err != nil {
		return "", err
	}
	owned, err := t.OwnsRun(r.Context(), r.PathValue("id"))
	if err != nil {
		return "", err
	}
	if !owned {
		return "", runNotFound(r.PathValue("id"))
	}
	return t.Team(), nil
}
