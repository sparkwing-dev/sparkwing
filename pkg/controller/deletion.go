package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
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
	// CacheURL reaches the cache service, which keeps each team's artifacts,
	// binaries and build cache in a tree of its own. The controller deletes a
	// tree with an operator grant signed by its cache grant key.
	CacheURL string
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
	if errors.Is(err, store.ErrOnlyTeam) || moneyInFlight(err) {
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

// safety: a session left open on a shared machine must not be able to delete
// the account behind it, so deletion asks for a fresh sign-in.
const accountDeletionSignInWindow = 10 * time.Minute

type deleteAccountReq struct {
	ConfirmEmail string `json:"confirm_email"`
}

type deletedAccountJSON struct {
	AccountID    string   `json:"account_id"`
	DeletedTeams []string `json:"deleted_teams"`
}

func (s *Server) handleDeleteMe(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	if time.Since(p.signedInAt) > accountDeletionSignInWindow {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "reauth_required", Principal: p.label(),
			Message: "deleting an account needs a sign-in from the last 10 minutes; sign out, sign in again and retry",
		})
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
	if err := s.deleteAccount(w, r, acct.ID, p.AccountID); err != nil {
		writePolicyError(w, err)
	}
}

func (s *Server) handleOperatorDeleteAccount(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	ref := strings.TrimSpace(authorized.Target.AccountID)
	id := ref
	if strings.Contains(ref, "@") {
		acct, err := s.store.AccountByEmail(r.Context(), ref)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return &apierr.Error{Code: apierr.NotFound, Message: "account not found"}
		case errors.Is(err, store.ErrAmbiguousAccount):
			return &apierr.Error{Code: apierr.Conflict, Message: err.Error()}
		case err != nil:
			return err
		}
		id = acct.ID
	}
	return s.deleteAccount(w, r, id, authorized.Principal.Capability.Subject.ID)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, accountID, by string) error {
	res, err := s.store.DeleteAccount(r.Context(), accountID, time.Now())
	var lastOwner *store.LastOwnerError
	switch {
	case moneyInFlight(err):
		return &apierr.Error{Code: apierr.Conflict, Message: err.Error()}
	case errors.As(err, &lastOwner):
		teams := make([]teamRefJSON, 0, len(lastOwner.Teams))
		for _, t := range lastOwner.Teams {
			teams = append(teams, teamRefJSON{Slug: string(t.Slug), DisplayName: t.Label(), Role: string(store.RoleOwner)})
		}
		return &apierr.Error{Code: apierr.Conflict, Message: "hand ownership of these teams to another member, or delete them, first", Details: map[string]any{"teams": teams}}
	case errors.Is(err, store.ErrNotFound):
		return &apierr.Error{Code: apierr.NotFound, Message: "account not found"}
	case errors.Is(err, store.ErrInvalidInput):
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	case err != nil:
		return err
	}
	s.invalidateRevokedTokens(res.RevokedPrefixes)
	out := deletedAccountJSON{AccountID: res.AccountID, DeletedTeams: make([]string, 0, len(res.DeletedTeams))}
	for _, t := range res.DeletedTeams {
		out.DeletedTeams = append(out.DeletedTeams, string(t))
	}
	s.logger.Info("account deleted", "account", res.AccountID, "by", by, "teams_deleted", len(res.DeletedTeams))
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleOperatorDeleteTeam(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	del, revoked, err := s.store.AsOperator().RequestTeamDeletion(r.Context(), store.Team(authorized.Target.Team), time.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return &apierr.Error{Code: apierr.NotFound, Message: "team is not registered"}
	case moneyInFlight(err):
		return &apierr.Error{Code: apierr.Conflict, Message: err.Error()}
	case errors.Is(err, store.ErrInvalidInput):
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	case err != nil:
		return err
	}
	s.invalidateRevokedTokens(revoked)
	s.logger.Info("team deletion requested", "team", string(del.Team), "by", authorized.Principal.Capability.Subject.ID)
	writeJSON(w, http.StatusAccepted, teamDeletionBody(del))
	return nil
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

// safety: another replica may still accept a revoked token from its cache,
// or write through a tenant handle it cached, until those caches lapse; the
// purge waits out twice the longer of them, so nothing written through a
// stale cache lands after the rows are gone.
var teamPurgeDelay = 2 * max(tokenCacheTTL, tenantCacheTTL)

// safety: a cache grant or log upload URL issued before the deletion is
// verified by its signature alone and writes into the team's storage until it
// expires, so the cache tree and logs are deleted once more after the
// longest-lived of them has lapsed.
const teamRecheckDelay = max(authwire.CacheGrantTTL, logSegmentURLTTL) + time.Hour

// safety: A stopped replica keeps a deletion lease only until another can resume it.
const teamDeletionLease = 5 * time.Minute

// ProcessTeamDeletions does, as of now, the work every team deletion has
// due. A pending deletion requested at least teamPurgeDelay earlier has its
// stored objects, then its rows, removed; a finished one whose recheck is due
// has its cache tree and rows swept once more. Each deletion is taken under a
// lease, renewed before every step that leaves the database, so one replica
// works on it at a time. A step that fails leaves the deletion as it was with
// the failure recorded, and the next pass retries it from the start, because
// every step removes only what is still there. ServeWith runs it on a timer;
// a process that serves Handler directly calls it.
func (s *Server) ProcessTeamDeletions(ctx context.Context, now time.Time) {
	op := s.store.AsOperator()
	pending, err := op.PendingTeamDeletions(ctx)
	if err != nil {
		s.logger.Warn("team deletions: list pending", "err", err)
		return
	}
	for _, d := range pending {
		recheck := d.State == store.TeamDeletionDone
		switch {
		case recheck && (d.RecheckAt == nil || now.Before(*d.RecheckAt)):
			continue
		case !recheck && now.Sub(d.RequestedAt) < teamPurgeDelay:
			continue
		}
		var err error
		if recheck {
			err = s.recheckTeam(ctx, d.Team, now)
		} else {
			err = s.purgeTeam(ctx, d.Team, now)
		}
		if errors.Is(err, errDeletionNotHeld) {
			continue
		}
		if err != nil {
			s.logger.Warn("team deletion incomplete; retrying next pass", "team", string(d.Team), "err", err)
			if rerr := op.RecordTeamDeletionFailure(ctx, d.Team, err.Error()); rerr != nil {
				s.logger.Warn("team deletions: record failure", "team", string(d.Team), "err", rerr)
			}
			continue
		}
		s.logger.Info("team deletion step finished", "team", string(d.Team), "recheck", recheck)
	}
}

var errDeletionNotHeld = errors.New("another replica holds this team deletion")

func (s *Server) holdDeletion(ctx context.Context, team store.Team, now time.Time) error {
	ok, err := s.store.AsOperator().ClaimTeamDeletion(ctx, team, s.deletionHolder(), now, teamDeletionLease)
	if err != nil {
		return err
	}
	if !ok {
		return errDeletionNotHeld
	}
	return nil
}

func (s *Server) deletionHolder() string {
	s.deletionHolderOnce.Do(func() {
		id, err := randomURLToken()
		if err != nil {
			id = fmt.Sprintf("controller-%d", time.Now().UnixNano())
		}
		s.deletionHolderID = id
	})
	return s.deletionHolderID
}

// safety: stored objects go before rows, because the team's rows are the
// only index of some of what the cache holds for it.
func (s *Server) purgeTeam(ctx context.Context, team store.Team, now time.Time) error {
	op := s.store.AsOperator()
	if err := s.holdDeletion(ctx, team, now); err != nil {
		return err
	}
	if err := s.purgeTeamLogs(ctx, team); err != nil {
		return err
	}
	if err := s.holdDeletion(ctx, team, now); err != nil {
		return err
	}
	if err := s.purgeTeamCache(ctx, team); err != nil {
		return err
	}
	if err := s.holdDeletion(ctx, team, now); err != nil {
		return err
	}
	return op.PurgeTeam(ctx, team, s.deletionHolder(), now, teamRecheckDelay)
}

func (s *Server) recheckTeam(ctx context.Context, team store.Team, now time.Time) error {
	if err := s.holdDeletion(ctx, team, now); err != nil {
		return err
	}
	if err := s.purgeTeamLogs(ctx, team); err != nil {
		return err
	}
	if err := s.holdDeletion(ctx, team, now); err != nil {
		return err
	}
	if err := s.purgeTeamCache(ctx, team); err != nil {
		return err
	}
	if err := s.holdDeletion(ctx, team, now); err != nil {
		return err
	}
	return s.store.AsOperator().FinishTeamRecheck(ctx, team, s.deletionHolder(), now)
}

func (s *Server) purgeTeamLogs(ctx context.Context, team store.Team) error {
	if objs := s.logObjects(); objs != nil {
		classes, err := s.teamLogClasses(ctx, objs, team)
		if err != nil {
			return err
		}
		for _, class := range classes {
			prefix := logseg.ClassPrefix(class) + "teams/" + string(team) + "/"
			if _, err := objs.deletePrefix(ctx, prefix); err != nil {
				return fmt.Errorf("delete the team's log segments under %s: %w", prefix, err)
			}
		}
	}
	return nil
}

// safety: an attempt keeps the class it was written under, so a class the operator has since retired still holds
// logs; the store's own class directories name it even once the configuration and the team's records no longer do.
func (s *Server) teamLogClasses(ctx context.Context, objs logObjectStore, team store.Team) ([]string, error) {
	classes := s.logClasses()
	held, err := objs.classes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the log retention classes: %w", err)
	}
	for _, class := range held {
		if _, valid := logseg.ClassDays(class); valid && !slices.Contains(classes, class) {
			classes = append(classes, class)
		}
	}
	return classes, nil
}

func deleteRemote(ctx context.Context, target, bearer string) error {
	// #nosec G704 -- the origin is operator configuration; the id is an escaped segment
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	// #nosec G704 -- the request keeps the operator-configured origin
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errors.Join(fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(body))), rerr)
	}
	return nil
}

func (s *Server) purgeTeamCache(ctx context.Context, team store.Team) error {
	ts := s.teamStorage
	if ts.CacheURL == "" {
		return nil
	}
	target := strings.TrimRight(ts.CacheURL, "/") + "/admin/teams/" + url.PathEscape(string(team))
	bearer, err := s.cacheOperatorBearer(time.Now())
	if err != nil {
		return fmt.Errorf("sign the cache deletion: %w", err)
	}
	if err := deleteRemote(ctx, target, bearer); err != nil {
		return fmt.Errorf("delete cache tree: %w", err)
	}
	return nil
}

// safety: An unsettled checkout or dispute hold blocks account deletion until money is resolved.
func moneyInFlight(err error) bool {
	return errors.Is(err, store.ErrOpenCheckout) || errors.Is(err, store.ErrTeamFrozen) ||
		errors.Is(err, store.ErrTeamOwes)
}
