package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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

// ErrStandingRequestClosed refuses a decision naming a request that is not the team's open one.
var ErrStandingRequestClosed = errors.New("the standing request is no longer open")

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

// RequestAuthorizedStanding records a request for High standing, higher limits or larger payments under current team administration.
func (t *Tenant) RequestAuthorizedStanding(ctx context.Context, a capability.Authorized, tier, message string) (StandingRequest, error) {
	message = strings.TrimSpace(message)
	if tier != "High" || message == "" || utf8.RuneCountInString(message) > StandingRequestMessageLimit {
		return StandingRequest{}, fmt.Errorf("%w: a standing request names the High tier and a message of up to %d characters", ErrInvalidInput, StandingRequestMessageLimit)
	}
	var out StandingRequest
	err := t.billingMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		actor := p.Capability.Subject.ID
		standing, err := trustStandingTx(ctx, tx, t.team, tx.now(ctx))
		if err != nil {
			return err
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
		now := tx.now(ctx)
		if _, err := tx.ExecContext(ctx, `INSERT INTO standing_requests(id,team,tier,message,requested_by,requested_at,state) VALUES (?,?,?,?,?,?,?)`, id, t.team, tier, message, actor, now.UnixNano(), StandingRequestOpen); err != nil {
			return err
		}
		if err := tx.operator.recordAudit(ctx, tx, auditContext{Team: t.team, Actor: auditPrincipalActor(p), At: now}, auditStandingRequest{RequestID: id, State: StandingRequestOpen, Tier: auditStanding(tier), Standing: auditStanding(standing.Tier), HistoryTier: auditStanding(standing.History.Tier)}); err != nil {
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
func (o *Operator) OpenStandingRequests(ctx context.Context, limit int) (_ []StandingRequest, err error) {
	rows, err := o.s.query(ctx, `SELECT `+standingRequestColumns+` WHERE r.state=? ORDER BY r.requested_at, r.id LIMIT ?`, StandingRequestOpen, limit)
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

func decideStandingRequestTx(ctx context.Context, tx *storeTx, team Team, id, state, actor, reason string, provenance auditActor) (StandingRequest, error) {
	r, err := scanStandingRequest(tx.QueryRowContext(ctx, `SELECT `+standingRequestColumns+` WHERE r.team=? AND r.id=?`, team, id))
	if errors.Is(err, sql.ErrNoRows) || err == nil && r.State != StandingRequestOpen {
		return r, ErrStandingRequestClosed
	}
	if err != nil {
		return r, err
	}
	now := tx.now(ctx)
	if _, err := tx.ExecContext(ctx, `UPDATE standing_requests SET state=?,decided_by=?,decided_at=?,decision_reason=? WHERE team=? AND id=? AND state=?`, state, actor, now.UnixNano(), reason, team, id, StandingRequestOpen); err != nil {
		return r, err
	}
	r.State, r.DecidedBy, r.DecidedAt, r.DecisionReason = state, actor, now, reason
	return r, tx.operator.recordAudit(ctx, tx, auditContext{Team: team, Actor: provenance, At: now}, auditStandingRequest{RequestID: id, State: auditRequestState(state), Tier: auditStanding(r.Tier)})
}

// ConsoleDeclineStandingRequest declines the named open standing request under current operator authority.
func (c *Console) ConsoleDeclineStandingRequest(ctx context.Context, a capability.Authorized, id, reason string) (StandingRequest, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor auditActor) (StandingRequest, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return StandingRequest{}, err
		}
		if reason, err = consoleReason(reason); err != nil {
			return StandingRequest{}, err
		}
		return decideStandingRequestTx(ctx, tx, t.team, id, StandingRequestDeclined, actor.subject.ID, reason, actor)
	})
}
