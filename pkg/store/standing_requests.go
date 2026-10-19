package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Standing request states.
const (
	StandingRequestOpen     = "open"
	StandingRequestApproved = "approved"
	StandingRequestDeclined = "declined"
)

// Business events for standing requests.
const (
	BusinessEventStandingRequested      = "billing.standing_requested"
	BusinessEventStandingRequestDecided = "billing.standing_request_decided"
)

// StandingRequestMessageLimit is the longest message a standing request carries.
const StandingRequestMessageLimit = 1000

// ErrStandingRequestOpen refuses a second request while the team's earlier one is open.
var ErrStandingRequestOpen = errors.New("the team already has an open standing request")

// StandingRequestInput is a team administrator's request for a higher standing tier.
type StandingRequestInput struct {
	Tier    string
	Message string
	Actor   string
}

// StandingRequest is a request for operator-granted standing and its outcome.
type StandingRequest struct {
	ID             string
	Team           Team
	Tier           string
	Message        string
	RequestedBy    string
	RequesterEmail string
	RequestedAt    time.Time
	State          string
	DecidedBy      string
	DecidedAt      time.Time
	DecisionReason string
}

const standingRequestColumns = `r.id,r.team,r.tier,r.message,r.requested_by,COALESCE(a.email,''),r.requested_at,r.state,r.decided_by,r.decided_at,r.decision_reason FROM standing_requests r LEFT JOIN accounts a ON a.id=r.requested_by`

func scanStandingRequest(row rowScanner) (StandingRequest, error) {
	var r StandingRequest
	var requested, decided int64
	if err := row.Scan(&r.ID, &r.Team, &r.Tier, &r.Message, &r.RequestedBy, &r.RequesterEmail, &requested, &r.State, &r.DecidedBy, &decided, &r.DecisionReason); err != nil {
		return r, err
	}
	r.RequestedAt = time.Unix(0, requested)
	if decided > 0 {
		r.DecidedAt = time.Unix(0, decided)
	}
	return r, nil
}

// RequestStanding records an open request for operator-granted standing.
func (t *Tenant) RequestStanding(ctx context.Context, in StandingRequestInput) (StandingRequest, error) {
	in.Message = strings.TrimSpace(in.Message)
	if in.Tier != "High" || in.Message == "" || len(in.Message) > StandingRequestMessageLimit || in.Actor == "" {
		return StandingRequest{}, fmt.Errorf("%w: a standing request names the High tier, its requester and a message of up to %d characters", ErrInvalidInput, StandingRequestMessageLimit)
	}
	var out StandingRequest
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if err := t.lockTeamTx(ctx, tx); err != nil {
			return err
		}
		standing, err := trustStandingTx(ctx, tx, t.team, tx.now())
		if err != nil {
			return err
		}
		if standing.Tier == "High" {
			return authorityError(apierr.Conflict, "the team already has High standing")
		}
		open, err := rowPresentTx(ctx, tx, `SELECT 1 FROM standing_requests WHERE team=? AND state=?`, t.team, StandingRequestOpen)
		if err != nil {
			return err
		}
		if open {
			return ErrStandingRequestOpen
		}
		id, err := newCreditID("standing")
		if err != nil {
			return err
		}
		now := tx.now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO standing_requests(id,team,tier,message,requested_by,requested_at,state) VALUES (?,?,?,?,?,?,?)`, id, t.team, in.Tier, in.Message, in.Actor, now.UnixNano(), StandingRequestOpen); err != nil {
			return err
		}
		if err := RecordBusinessEvent(tx, BusinessEvent{Kind: BusinessEventStandingRequested, Team: t.team, SubjectID: id, Actor: in.Actor, At: now, Attrs: map[string]any{"tier": in.Tier, "message": in.Message, "standing": standing.Tier, "history_tier": standing.History.Tier}}); err != nil {
			return err
		}
		out, err = scanStandingRequest(tx.QueryRowContext(ctx, `SELECT `+standingRequestColumns+` WHERE r.team=? AND r.id=?`, t.team, id))
		return err
	})
	return out, err
}

// LatestStandingRequest returns the team's most recent standing request.
func (t *Tenant) LatestStandingRequest(ctx context.Context) (StandingRequest, bool, error) {
	r, err := scanStandingRequest(t.s.queryRow(ctx, `SELECT `+standingRequestColumns+` WHERE r.team=? ORDER BY r.requested_at DESC, r.id DESC LIMIT 1`, t.team))
	if errors.Is(err, sql.ErrNoRows) {
		return StandingRequest{}, false, nil
	}
	return r, err == nil, err
}

// OpenStandingRequests lists every team's open standing requests, oldest first.
func (s *Store) OpenStandingRequests(ctx context.Context, limit int) (_ []StandingRequest, err error) {
	rows, err := s.query(ctx, `SELECT `+standingRequestColumns+` WHERE r.state=? ORDER BY r.requested_at, r.id LIMIT ?`, StandingRequestOpen, limit)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []StandingRequest{}
	for rows.Next() {
		r, err := scanStandingRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func decideStandingRequestTx(ctx context.Context, tx *storeTx, team Team, state, actor, reason string) (StandingRequest, bool, error) {
	r, err := scanStandingRequest(tx.QueryRowContext(ctx, `SELECT `+standingRequestColumns+` WHERE r.team=? AND r.state=?`, team, StandingRequestOpen))
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	now := tx.now()
	if _, err := tx.ExecContext(ctx, `UPDATE standing_requests SET state=?,decided_by=?,decided_at=?,decision_reason=? WHERE team=? AND id=?`, state, actor, now.UnixNano(), reason, team, r.ID); err != nil {
		return r, false, err
	}
	r.State, r.DecidedBy, r.DecidedAt, r.DecisionReason = state, actor, now, reason
	err = RecordBusinessEvent(tx, BusinessEvent{Kind: BusinessEventStandingRequestDecided, Team: team, SubjectID: r.ID, Actor: actor, At: now, Attrs: map[string]any{"tier": r.Tier, "state": state, "reason": reason}})
	return r, true, err
}

// ConsoleDeclineStandingRequest declines the team's open standing request under current operator authority.
func (c *Console) ConsoleDeclineStandingRequest(ctx context.Context, a capability.Authorized, reason string) (StandingRequest, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor string) (StandingRequest, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return StandingRequest{}, err
		}
		if reason, err = consoleReason(reason); err != nil {
			return StandingRequest{}, err
		}
		r, found, err := decideStandingRequestTx(ctx, tx, t.team, StandingRequestDeclined, actor, reason)
		if err == nil && !found {
			err = authorityError(apierr.NotFound, "the team has no open standing request")
		}
		return r, err
	})
}
