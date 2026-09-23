package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TeamDeletionInterval is how often the controller finishes pending team
// deletions. A request closes the team at once; this pass removes its rows
// and stored objects.
const TeamDeletionInterval = time.Minute

// TeamStorage names the services outside the database that hold a team's
// data, and the operator credentials the controller deletes it with. A
// service left empty holds nothing for this controller to delete.
type TeamStorage struct {
	// LogsURL and LogsToken reach the logs service, which keeps node logs
	// under bare run ids and deletes them for an admin-scoped bearer.
	LogsURL   string
	LogsToken string
	// CacheURL and CacheToken reach the cache service, which keeps each
	// team's artifacts, binaries and build cache in a tree of its own.
	CacheURL   string
	CacheToken string
}

// WithTeamStorage tells the controller where a team's stored objects live,
// so deleting a team removes them along with its rows.
func (s *Server) WithTeamStorage(ts TeamStorage) *Server {
	s.teamStorage = ts
	return s
}

type teamDeletionJSON struct {
	Slug        string `json:"slug"`
	State       string `json:"state"`
	RequestedAt int64  `json:"requested_at"`
	FinishedAt  *int64 `json:"finished_at"`
	Attempts    int    `json:"attempts"`
	LastError   string `json:"last_error"`
}

func teamDeletionBody(d store.TeamDeletion) teamDeletionJSON {
	out := teamDeletionJSON{
		Slug: string(d.Team), State: d.State, RequestedAt: d.RequestedAt.Unix(),
		Attempts: d.Attempts, LastError: d.LastError,
	}
	if d.FinishedAt != nil {
		v := d.FinishedAt.Unix()
		out.FinishedAt = &v
	}
	return out
}

type deleteTeamReq struct {
	ConfirmSlug string `json:"confirm_slug"`
}

// safety: the slug is typed back rather than implied by the session, so a
// stale tab open on another team cannot delete the team it no longer shows.
func (s *Server) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req deleteTeamReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if store.NormalizeTeam(store.Team(req.ConfirmSlug)) != p.Team {
		writeError(w, http.StatusBadRequest, errors.New("confirm_slug does not match the active team's slug"))
		return
	}
	del, revoked, err := t.RequestDeletion(r.Context(), p.AccountID, time.Now())
	if errors.Is(err, store.ErrOnlyTeam) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeIdentityError(w, s, r, "delete team", err)
		return
	}
	s.invalidateRevokedTokens(revoked)
	s.logger.Info("team deletion requested", "team", string(del.Team), "by", p.AccountID)
	writeJSON(w, http.StatusAccepted, teamDeletionBody(del))
}

func (s *Server) handleMyTeamDeletions(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	dels, err := s.store.TeamDeletionsRequestedBy(r.Context(), p.AccountID)
	if err != nil {
		s.writeInternalError(w, r, "list team deletions", err)
		return
	}
	out := make([]teamDeletionJSON, 0, len(dels))
	for _, d := range dels {
		out = append(out, teamDeletionBody(d))
	}
	writeJSON(w, http.StatusOK, out)
}

type deleteAccountReq struct {
	ConfirmEmail string `json:"confirm_email"`
}

type deletedAccountJSON struct {
	AccountID    string   `json:"account_id"`
	DeletedTeams []string `json:"deleted_teams"`
}

type lastOwnerRefusalJSON struct {
	Error string        `json:"error"`
	Teams []teamRefJSON `json:"teams"`
}

func (s *Server) handleDeleteMe(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	var req deleteAccountReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	acct, err := s.store.Account(r.Context(), p.AccountID)
	if err != nil {
		s.writeInternalError(w, r, "delete account", err)
		return
	}
	if store.NormalizeEmail(req.ConfirmEmail) != acct.Email {
		writeError(w, http.StatusBadRequest, errors.New("confirm_email does not match the account's email"))
		return
	}
	s.deleteAccount(w, r, acct.ID, "self")
}

// handleOperatorDeleteAccount carries out a deletion request that reached
// the operator by mail. The path names the account by id or by email.
func (s *Server) handleOperatorDeleteAccount(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.PathValue("account"))
	id := ref
	if strings.Contains(ref, "@") {
		acct, err := s.store.AccountByEmail(r.Context(), ref)
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, fmt.Errorf("no account holds %s", ref))
			return
		case errors.Is(err, store.ErrAmbiguousAccount):
			writeError(w, http.StatusConflict, err)
			return
		case err != nil:
			s.writeInternalError(w, r, "operator delete account", err)
			return
		}
		id = acct.ID
	}
	s.deleteAccount(w, r, id, "operator")
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, accountID, by string) {
	res, err := s.store.DeleteAccount(r.Context(), accountID, time.Now())
	var lastOwner *store.LastOwnerError
	switch {
	case errors.As(err, &lastOwner):
		body := lastOwnerRefusalJSON{
			Error: "hand ownership of these teams to another member, or delete them, first",
			Teams: make([]teamRefJSON, 0, len(lastOwner.Teams)),
		}
		for _, t := range lastOwner.Teams {
			body.Teams = append(body.Teams, teamRefJSON{Slug: string(t.Slug), DisplayName: t.Label(), Role: string(store.RoleOwner)})
		}
		writeJSON(w, http.StatusConflict, body)
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errors.New("no such account"))
		return
	case err != nil:
		s.writeInternalError(w, r, "delete account", err)
		return
	}
	s.invalidateRevokedTokens(res.RevokedPrefixes)
	out := deletedAccountJSON{AccountID: res.AccountID, DeletedTeams: make([]string, 0, len(res.DeletedTeams))}
	for _, t := range res.DeletedTeams {
		out.DeletedTeams = append(out.DeletedTeams, string(t))
	}
	s.logger.Info("account deleted", "account", res.AccountID, "by", by, "teams_deleted", len(res.DeletedTeams))
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleOperatorDeleteTeam(w http.ResponseWriter, r *http.Request) {
	del, revoked, err := s.store.AsOperator().RequestTeamDeletion(r.Context(), store.Team(r.PathValue("team")), time.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, fmt.Errorf("team %q is not registered", r.PathValue("team")))
		return
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
		return
	case err != nil:
		s.writeInternalError(w, r, "operator delete team", err)
		return
	}
	s.invalidateRevokedTokens(revoked)
	s.logger.Info("team deletion requested", "team", string(del.Team), "by", "operator")
	writeJSON(w, http.StatusAccepted, teamDeletionBody(del))
}

func (s *Server) runTeamDeletions(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.ProcessTeamDeletions(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ProcessTeamDeletions finishes, as of now, every pending team deletion
// requested at least one tenant-cache lifetime earlier: it removes each
// team's stored objects, then its rows. A deletion that fails stays pending
// with the failure recorded, and the next pass retries it from the start,
// because every step removes only what is still there. ServeWith runs it on
// a timer; a process that serves Handler directly calls it.
func (s *Server) ProcessTeamDeletions(ctx context.Context, now time.Time) {
	op := s.store.AsOperator()
	pending, err := op.PendingTeamDeletions(ctx)
	if err != nil {
		s.logger.Warn("team deletions: list pending", "err", err)
		return
	}
	for _, d := range pending {
		// safety: a replica may still hold a tenant handle cached before the
		// request and write through it until the handle lapses; purging after
		// that leaves no such row behind under a slug someone may take next.
		if now.Sub(d.RequestedAt) < tenantCacheTTL {
			continue
		}
		if err := s.purgeTeam(ctx, d.Team); err != nil {
			s.logger.Warn("team deletion incomplete; retrying next pass", "team", string(d.Team), "err", err)
			if rerr := op.RecordTeamDeletionFailure(ctx, d.Team, err.Error()); rerr != nil {
				s.logger.Warn("team deletions: record failure", "team", string(d.Team), "err", rerr)
			}
			continue
		}
		s.logger.Info("team deleted", "team", string(d.Team))
	}
}

// safety: stored objects go before rows, because the run rows are the only
// index of what the logs service holds for the team.
func (s *Server) purgeTeam(ctx context.Context, team store.Team) error {
	op := s.store.AsOperator()
	runIDs, err := op.TeamRunIDs(ctx, team)
	if err != nil {
		return err
	}
	if err := s.purgeTeamLogs(ctx, runIDs); err != nil {
		return err
	}
	if err := s.purgeTeamCache(ctx, team); err != nil {
		return err
	}
	return op.PurgeTeam(ctx, team, time.Now())
}

func (s *Server) purgeTeamLogs(ctx context.Context, runIDs []string) error {
	ts := s.teamStorage
	if ts.LogsURL == "" || len(runIDs) == 0 {
		return nil
	}
	if ts.LogsToken == "" {
		return errors.New("a logs service is configured but the controller holds no admin credential to delete logs with")
	}
	client := logs.NewClientWithToken(strings.TrimRight(ts.LogsURL, "/"), nil, ts.LogsToken)
	for _, id := range runIDs {
		if err := client.DeleteRun(ctx, id); err != nil {
			return fmt.Errorf("delete logs of run %s: %w", id, err)
		}
	}
	return nil
}

func (s *Server) purgeTeamCache(ctx context.Context, team store.Team) error {
	ts := s.teamStorage
	if ts.CacheURL == "" {
		return nil
	}
	u := strings.TrimRight(ts.CacheURL, "/") + "/admin/teams/" + url.PathEscape(string(team))
	// #nosec G704 -- the origin is operator configuration; the team is an escaped slug
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	if ts.CacheToken != "" {
		req.Header.Set("Authorization", "Bearer "+ts.CacheToken)
	}
	// #nosec G704 -- the request keeps the operator-configured origin
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("delete cache tree: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errors.Join(fmt.Errorf("delete cache tree: %d %s", resp.StatusCode, strings.TrimSpace(string(body))), rerr)
	}
	return nil
}
