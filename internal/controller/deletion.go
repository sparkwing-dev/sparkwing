package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// teamDeletionInterval is how often the controller finishes pending team
// deletions. A request closes the team at once; this pass removes its rows
// and stored objects.
const teamDeletionInterval = time.Minute

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

func (s *Server) handleDeleteTeam(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req deleteTeamReq
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.ConfirmSlug) == "" || store.NormalizeTeam(store.Team(req.ConfirmSlug)) != store.Team(a.Target.Team) {
		return invalidEngineBody("confirm_slug does not match the active team's slug")
	}
	writer, err := s.teamWriter(r, a)
	if err != nil {
		return err
	}
	del, err := writer.DeleteAuthorizedTeam(r.Context(), a)
	if err != nil {
		if errors.Is(err, store.ErrOnlyTeam) {
			return &apierr.Error{Code: apierr.Conflict, Message: err.Error()}
		}
		return accountMutationError(err)
	}
	if del.Team != store.Team(a.Target.Team) {
		return routepolicy.MissingEndpointPolicy()
	}
	s.logger.Info("team deletion requested", "team", string(del.Team), "by", a.Principal.Capability.Subject.ID)
	writeJSON(w, http.StatusAccepted, teamDeletionBody(del))
	return nil
}

func (s *Server) handleMyTeamDeletions(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	reader, err := s.accountReader()
	if err != nil {
		return err
	}
	dels, err := reader.TeamDeletionsRequestedBy(r.Context(), a.Principal.Capability.Subject.ID)
	if err != nil {
		return engineLookupError(err)
	}
	out := make([]teamDeletionJSON, 0, len(dels))
	for _, d := range dels {
		out = append(out, teamDeletionBody(d))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type deletedAccountJSON struct {
	AccountID    string   `json:"account_id"`
	DeletedTeams []string `json:"deleted_teams"`
}

func (s *Server) handleOperatorDeleteAccount(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	writer, err := s.consoleWriter()
	if err != nil {
		return err
	}
	res, err := writer.ConsoleDeleteAccount(r.Context(), authorized)
	var lastOwner *store.LastOwnerError
	switch {
	case errors.As(err, &lastOwner):
		teams := make([]teamRefJSON, 0, len(lastOwner.Teams))
		for _, t := range lastOwner.Teams {
			teams = append(teams, teamRefJSON{Slug: string(t.Slug), DisplayName: t.Label(), Role: string(store.RoleOwner)})
		}
		return &apierr.Error{Code: apierr.Conflict, Message: "hand ownership of these teams to another member, or delete them, first", Details: map[string]any{"teams": teams}}
	case errors.Is(err, store.ErrAmbiguousAccount):
		return &apierr.Error{Code: apierr.Conflict, Message: "account email matches more than one account"}
	case errors.Is(err, store.ErrNotFound):
		return &apierr.Error{Code: apierr.NotFound, Message: "account not found"}
	case errors.Is(err, store.ErrInvalidInput):
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	case err != nil:
		return err
	}
	out := deletedAccountJSON{AccountID: res.AccountID, DeletedTeams: make([]string, 0, len(res.DeletedTeams))}
	for _, t := range res.DeletedTeams {
		out.DeletedTeams = append(out.DeletedTeams, string(t))
	}
	s.logger.Info("account deleted", "account", res.AccountID, "by", authorized.Principal.Capability.Subject.ID, "teams_deleted", len(res.DeletedTeams))
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleOperatorDeleteTeam(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	writer, err := s.consoleWriter()
	if err != nil {
		return err
	}
	del, err := writer.ConsoleDeleteTeam(r.Context(), authorized)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return &apierr.Error{Code: apierr.NotFound, Message: "team is not registered"}
	case errors.Is(err, store.ErrInvalidInput):
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	case err != nil:
		return err
	}
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

var teamPurgeDelay = time.Minute

const teamRecheckDelay = limits.LogUploadURLTTL + time.Hour

// safety: A stopped replica keeps a deletion lease only until another can resume it.
const teamDeletionLease = 5 * time.Minute

// ProcessTeamDeletions does, as of now, the work every team deletion has
// due. A pending deletion requested at least teamPurgeDelay earlier has its
// stored objects, then its rows, removed; a finished one whose recheck is due
// has its stored objects and rows swept once more. Each deletion is taken under a
// lease, renewed before every step that leaves the database, so one replica
// works on it at a time. A step that fails leaves the deletion as it was with
// the failure recorded, and the next pass retries it from the start, because
// every step removes only what is still there. ServeWith runs it on a timer;
// a process that serves Handler directly calls it.
func (s *Server) ProcessTeamDeletions(ctx context.Context, now time.Time) {
	op := s.operator
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
	ok, err := s.operator.ClaimTeamDeletion(ctx, team, s.deletionHolder(), teamDeletionLease)
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
	op := s.operator
	if err := s.holdDeletion(ctx, team, now); err != nil {
		return err
	}
	if err := s.purgeTeamLogs(ctx, team); err != nil {
		return err
	}
	if err := s.holdDeletion(ctx, team, now); err != nil {
		return err
	}
	return op.PurgeTeam(ctx, team, s.deletionHolder(), teamRecheckDelay)
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
	return s.operator.FinishTeamRecheck(ctx, team, s.deletionHolder())
}

func (s *Server) purgeTeamLogs(ctx context.Context, team store.Team) error {
	if s.dataController == nil || s.dataController.Objects == nil {
		return nil
	}
	objects, ok := s.dataController.Objects.(blob.TeamObjectDeleter)
	if !ok {
		return fmt.Errorf("data backend cannot delete team objects")
	}
	return objects.DeleteTeamObjects(ctx, string(team))
}
