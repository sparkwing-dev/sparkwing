package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// DeviceLogin is one device sign-in request. Request is the device's own description of what it asks for,
// which the controller owns; the store keeps it opaque.
type DeviceLogin struct {
	ID        string
	UserCode  string
	Request   json.RawMessage
	CreatedAt time.Time
	ExpiresAt time.Time
	Team      Team
	AccountID string
	Session   capability.AuthorityRef
	Grants    []capability.Grant
}

// DevicePollStatus is the answer to a device's poll that is not a refusal.
type DevicePollStatus string

const (
	DevicePending  DevicePollStatus = "pending"
	DeviceSlowDown DevicePollStatus = "slow_down"
	DeviceApproved DevicePollStatus = "approved"
)

// DevicePoll carries the next interval, and the approved login when Status is DeviceApproved.
type DevicePoll struct {
	Status   DevicePollStatus
	Interval time.Duration
	Login    DeviceLogin
}

// Device login refusals. ErrNotFound covers an unknown code and one already redeemed.
var (
	ErrDeviceLoginDenied  = errors.New("store: the device login was denied")
	ErrDeviceLoginExpired = errors.New("store: the device login expired")
	ErrDeviceCodeTaken    = errors.New("store: the device user code is in use")
)

// hack: RFC 8628 section 3.5 adds five seconds for each early poll.
const (
	deviceSlowDownStep  = 5 * time.Second
	deviceMaxPollPeriod = time.Minute
)

// CreateDeviceLogin records a pending device login under the digest of its device code.
func (o *Operator) CreateDeviceLogin(ctx context.Context, device [32]byte, userCode string, request json.RawMessage, ttl, interval time.Duration) (DeviceLogin, error) {
	if userCode == "" || len(request) == 0 || ttl <= 0 || interval <= 0 {
		return DeviceLogin{}, ErrInvalidInput
	}
	id, err := newCreditID("device")
	if err != nil {
		return DeviceLogin{}, err
	}
	var out DeviceLogin
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		now := tx.now(ctx)
		result, err := tx.execContext(ctx, `INSERT INTO device_logins(id,device_digest,user_code,request,state,created_at,expires_at,poll_interval) VALUES (?,?,?,?,'pending',?,?,?) ON CONFLICT DO NOTHING`,
			id, hex.EncodeToString(device[:]), userCode, string(request), now.UnixNano(), now.Add(ttl).UnixNano(), int64(interval))
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return errors.Join(ErrDeviceCodeTaken, err)
		}
		out = DeviceLogin{ID: id, UserCode: userCode, Request: request, CreatedAt: now, ExpiresAt: now.Add(ttl)}
		return nil
	})
	return out, err
}

// PendingDeviceLogin reads an undecided, unexpired login by its user code. Any other code reads as ErrNotFound,
// so a guess learns nothing about codes that expired or were decided.
func (o *Operator) PendingDeviceLogin(ctx context.Context, userCode string) (DeviceLogin, error) {
	var out DeviceLogin
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		var request string
		var created, expires int64
		err := tx.QueryRowContext(ctx, `SELECT id,request,created_at,expires_at FROM device_logins WHERE user_code=? AND state='pending' AND expires_at>?`, userCode, tx.now(ctx).UnixNano()).
			Scan(&out.ID, &request, &created, &expires)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		out.UserCode, out.Request = userCode, json.RawMessage(request)
		out.CreatedAt, out.ExpiresAt = time.Unix(0, created).UTC(), time.Unix(0, expires).UTC()
		return err
	})
	return out, err
}

// DecideDeviceLogin approves or denies a pending login once, from a direct account session. An approval records
// the session as the authority the device's credential will be minted under, and the grants the approver saw.
func (o *Operator) DecideDeviceLogin(ctx context.Context, p capability.Principal, userCode string, grants []capability.Grant, approve bool) (DeviceLogin, error) {
	if !p.DirectAccountSession() || p.Capability.Team == "" {
		return DeviceLogin{}, authorityError(apierr.Forbidden, "approving a device requires a direct account sign-in session")
	}
	state, decision := "denied", "denied"
	if approve {
		state, decision = "approved", "approved"
	}
	encoded, err := json.Marshal(grants)
	if err != nil {
		return DeviceLogin{}, err
	}
	var out DeviceLogin
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		now := tx.now(ctx)
		result, err := tx.execContext(ctx, `UPDATE device_logins SET state=?,team=?,account_id=?,session_hash=?,grants=?,decided_at=? WHERE user_code=? AND state='pending' AND expires_at>?`,
			state, p.Capability.Team, p.Capability.Subject.ID, p.Authority.ID, string(encoded), now.UnixNano(), userCode, now.UnixNano())
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return errors.Join(ErrNotFound, err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM device_logins WHERE user_code=?`, userCode).Scan(&out.ID); err != nil {
			return err
		}
		out.UserCode, out.Team, out.AccountID, out.Session, out.Grants = userCode, Team(p.Capability.Team), p.Capability.Subject.ID, p.Authority, grants
		return o.recordAudit(ctx, tx, auditContext{At: now, Team: Team(p.Capability.Team), Actor: auditPrincipalActor(p)}, auditDeviceLogin{LoginID: out.ID, Decision: decision})
	})
	return out, err
}

// PollDeviceLogin answers a device polling with its device code. An approved login is handed over once: the same
// transaction marks it redeemed, so a second poll reads ErrNotFound.
func (o *Operator) PollDeviceLogin(ctx context.Context, device [32]byte) (DevicePoll, error) {
	var out DevicePoll
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		now := tx.now(ctx)
		var id, state, request, grants string
		var expires, interval int64
		var polled sql.NullInt64
		var team, account, session sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT id,state,request,expires_at,poll_interval,polled_at,team,account_id,session_hash,grants FROM device_logins WHERE device_digest=?`, hex.EncodeToString(device[:])).
			Scan(&id, &state, &request, &expires, &interval, &polled, &team, &account, &session, &grants)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		switch {
		case now.UnixNano() >= expires:
			return ErrDeviceLoginExpired
		case state == "denied":
			return ErrDeviceLoginDenied
		case state != "pending" && state != "approved":
			return ErrNotFound
		}
		period := time.Duration(interval)
		out.Status = DevicePending
		if polled.Valid && now.Sub(time.Unix(0, polled.Int64)) < period {
			period = min(period+deviceSlowDownStep, deviceMaxPollPeriod)
			out.Status = DeviceSlowDown
		}
		out.Interval = period
		if _, err := tx.execContext(ctx, `UPDATE device_logins SET polled_at=?,poll_interval=? WHERE id=?`, now.UnixNano(), int64(period), id); err != nil {
			return err
		}
		if out.Status == DeviceSlowDown || state == "pending" {
			return nil
		}
		result, err := tx.execContext(ctx, `UPDATE device_logins SET state='redeemed' WHERE id=? AND state='approved'`, id)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return errors.Join(ErrNotFound, err)
		}
		var decided []capability.Grant
		if err := json.Unmarshal([]byte(grants), &decided); err != nil {
			return fmt.Errorf("read device login grants: %w", err)
		}
		out.Status = DeviceApproved
		out.Login = DeviceLogin{
			ID: id, Request: json.RawMessage(request), ExpiresAt: time.Unix(0, expires).UTC(), Team: Team(team.String), AccountID: account.String,
			Session: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: session.String}, Grants: decided,
		}
		return nil
	})
	return out, err
}

// CancelDeviceLogin lets the device withdraw its own request before it is redeemed.
func (o *Operator) CancelDeviceLogin(ctx context.Context, device [32]byte) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		result, err := tx.execContext(ctx, `UPDATE device_logins SET state='denied',decided_at=? WHERE device_digest=? AND state IN ('pending','approved')`, tx.now(ctx).UnixNano(), hex.EncodeToString(device[:]))
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return errors.Join(ErrNotFound, err)
		}
		return nil
	})
}
