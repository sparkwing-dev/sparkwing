package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
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

// safety: no principal, or a teamless peer or pre-team token, acts for the default team every such
// row was migrated into; a signed-in account never falls back, so one that lost its team
// reaches no team's rows.
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

// safety: every tenant-owned read and write goes through here, and the team never comes from the
// request body, path or query.
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

// safety: the mux unescapes every segment before matching, so /api/v1/%72uns/x
// reaches a /api/v1/runs/{id} handler. Reading the id at the {id} segment of
// the matched pattern, unescaped as the mux does, keeps the run checked here
// the run the handler is handed, and a path too short to name one checks the
// empty id rather than passing on.
func boundaryRunID(pattern string, r *http.Request) string {
	_, path, _ := strings.Cut(pattern, " ")
	at := slices.Index(strings.Split(path, "/"), "{id}")
	segs := strings.Split(r.URL.EscapedPath(), "/")
	if at < 0 || len(segs) <= at {
		return ""
	}
	id, err := url.PathUnescape(segs[at])
	if err != nil {
		return segs[at]
	}
	return id
}

// safety: it wraps the mux rather than each route, and reads each route's
// tenant resource from tenantRoutes by the pattern the mux matched, so a
// route missing from the table is refused instead of served unchecked. A run
// or trigger of another team answers as a missing run, so it looks like no run.
func (s *Server) teamBoundary(mux *http.ServeMux, next http.Handler) http.Handler {
	return s.boundaryOver(tenantRoutes, mux, next)
}

func (s *Server) boundaryOver(routes map[string]tenantResource, mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		// safety: no match leaves the fallback to answer 404 or 405, and a
		// pattern with no method is the path a trailing-slash redirect names,
		// which reaches no handler before the client asks again.
		if !strings.Contains(pattern, " ") {
			next.ServeHTTP(w, r)
			return
		}
		res, listed := routes[pattern]
		if !listed {
			s.writeInternalError(w, r, "team boundary", fmt.Errorf("route %q declares no tenant resource", pattern))
			return
		}
		switch res.owner {
		case ownerNone, ownerAccount, ownerDeployment:
		case ownerTeam:
			if _, ok := s.requestTenant(w, r); !ok {
				return
			}
		case ownerRun:
			if !s.ownsBoundaryRun(w, r, pattern) {
				return
			}
		default:
			s.writeInternalError(w, r, "team boundary", fmt.Errorf("route %q: %s has no ownership rule", pattern, res.name))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ownsBoundaryRun(w http.ResponseWriter, r *http.Request, pattern string) bool {
	runID := boundaryRunID(pattern, r)
	t, ok := s.requestTenant(w, r)
	if !ok {
		return false
	}
	owned, err := t.OwnsRun(r.Context(), runID)
	if err != nil {
		s.writeInternalError(w, r, "team boundary", err)
		return false
	}
	if !owned {
		writeError(w, http.StatusNotFound, runNotFound(runID))
		return false
	}
	return true
}

func runNotFound(runID string) error {
	return fmt.Errorf("run %q: %w", runID, store.ErrNotFound)
}

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

// safety: repeats the team boundary's check because a grant it feeds opens that team's namespace
// in the shared cache.
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

// safety: a sweep names a trigger id with no request behind it, so the trigger
// is read through the team that owns the trigger rather than any team.
func (s *Server) triggerAcrossTeams(ctx context.Context, id string) (*store.Trigger, error) {
	team, err := s.store.AsOperator().TriggerTeam(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.triggerInTeam(ctx, team, id)
}

// safety: a run's outcome is reported from the trigger in the run's own team;
// another team may hold a trigger under the same id, and its check is not this
// run's to touch.
func (s *Server) runTrigger(ctx context.Context, runID string) (*store.Trigger, error) {
	team, err := s.store.AsOperator().RunTeam(ctx, runID)
	if err != nil {
		return nil, err
	}
	return s.triggerInTeam(ctx, team, runID)
}

func (s *Server) triggerInTeam(ctx context.Context, team store.Team, id string) (*store.Trigger, error) {
	tenant, err := s.tenantForTeam(ctx, team)
	if err != nil {
		return nil, err
	}
	return tenant.GetTrigger(ctx, id)
}
