package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// ReadBlob returns live metadata after rechecking tenant authority and read edges.
func (t *Tenant) ReadBlob(ctx context.Context, authority blob.Authority, digest blob.Digest) (blob.Blob, error) {
	return inTxResult(ctx, t.s, func(tx *storeTx) (blob.Blob, error) {
		presence, err := readableBlobsTx(ctx, tx, t.team, blob.HasManyRequest{Authority: authority, Digests: []blob.Digest{digest}})
		if err != nil {
			return blob.Blob{}, err
		}
		if len(presence.Present) != 1 {
			return blob.Blob{}, blob.ErrNotFound
		}
		var size int64
		err = tx.QueryRowContext(ctx, `SELECT b.size FROM blobs b WHERE b.team=? AND b.digest=?
AND NOT EXISTS (SELECT 1 FROM blob_tombstones t WHERE t.team=b.team AND t.digest=b.digest)`, t.team, digest).Scan(&size)
		if errors.Is(err, sql.ErrNoRows) {
			return blob.Blob{}, blob.ErrNotFound
		}
		if err != nil {
			return blob.Blob{}, err
		}
		if size < 0 {
			return blob.Blob{}, blob.ErrIntegrity
		}
		return blob.Blob{Team: string(t.team), Digest: digest, Size: size}, nil
	})
}

// SetComputeLimits validates and writes every named guard in one transaction.
// A value of zero removes that ceiling.
func (o *Operator) SetComputeLimits(ctx context.Context, updates map[string]int64) (_ ComputeLimits, err error) {
	var out ComputeLimits
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		out, err = setComputeLimitsTx(ctx, tx, updates)
		return err
	})
	return out, err
}

// AccountByEmail finds the account an address names: the one holding it
// verified, or the only one holding it at all.
func (s *Store) AccountByEmail(ctx context.Context, email string) (_ Account, err error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return Account{}, err
	}
	defer rollbackOrLog(tx)
	out, err := accountByEmailTx(ctx, tx, email)
	if err != nil {
		return Account{}, err
	}
	return out, tx.Commit()
}

// DeleteAccount removes an account and everything that identifies it: its
// identities, memberships, sessions and account row. Tokens it minted in any
// team are revoked. Teams it was the only member of are queued for deletion
// with all their data. Rows that belong to a team and name the account, such
// as the runs it started, stay with the team and name [deletedUserLabel]
// instead.
//
// It refuses with a [*LastOwnerError] while the account is the last owner of
// a team that has other members, because deleting it would leave those
// members in a team nobody can administer.
func (s *Store) DeleteAccount(ctx context.Context, accountID string) (AccountDeletion, error) {
	var result AccountDeletion
	err := s.inTx(ctx, func(tx *storeTx) error {
		var err error
		result, err = tx.operator.deleteAccountTx(ctx, tx, accountID, tx.now(ctx))
		return err
	})
	return result, err
}

// ChargeDownload admits under the shared quota lock and records the day's usage atomically.
func (t *Tenant) ChargeDownload(ctx context.Context, c DownloadCharge) (DownloadCharged, error) {
	if c.Bytes < 0 || c.FreeCapBytes < 0 || c.FundedCapBytes < 0 {
		return DownloadCharged{}, fmt.Errorf("%w: invalid download charge", ErrInvalidInput)
	}
	return inTxResult(ctx, t.s, func(tx *storeTx) (DownloadCharged, error) {
		return t.chargeDownloadTx(ctx, tx, c)
	})
}

// EgressUsage sums this team's daily egress over a UTC day or month.
func (t *Tenant) EgressUsage(ctx context.Context, period string) (EgressUsage, error) {
	start, end, err := egressPeriod(period)
	if err != nil {
		return EgressUsage{}, err
	}
	out := EgressUsage{Team: t.team, Period: period}
	var updated, invalid int64
	err = t.s.queryRow(ctx, `SELECT COALESCE(SUM(bytes),0),COALESCE(MAX(updated_at),0),COALESCE(SUM(CASE WHEN bytes<0 THEN 1 ELSE 0 END),0) FROM egress_usage WHERE team=? AND period>=? AND period<?`, t.team, start, end).Scan(&out.Bytes, &updated, &invalid)
	out.UpdatedAt = time.Unix(0, updated).UTC()
	if err == nil && (invalid != 0 || out.Bytes < 0) {
		err = errors.New("egress has invalid usage counters")
	}
	return out, err
}

// GitHubAppExtraRepos returns approved extras for a numeric source repository ID.
func (t *Tenant) GitHubAppExtraRepos(ctx context.Context, repositoryID int64) (_ []string, err error) {
	rows, err := t.s.query(ctx, `
		SELECT extra_repo FROM github_app_extra_repos WHERE team = ? AND repository_id = ? ORDER BY extra_repo`,
		string(t.team), repositoryID)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []string{}
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SettleQuota commits actual usage or releases the reservation in one transaction.
func (t *Tenant) SettleQuota(ctx context.Context, reservationID string, used int64) error {
	return t.settleQuota(ctx, reservationID, used)
}

// ReserveQuota locks one shared team/quota row; log reads are exempt from download quota.
func (t *Tenant) ReserveQuota(ctx context.Context, quota string, amount int64) (QuotaReservation, error) {
	return t.reserveQuota(ctx, quota, amount)
}

// SetCreditSettings atomically updates billing policy while preserving an installed rate table.
func (o *Operator) SetCreditSettings(ctx context.Context, up CreditSettingsUpdate) (_ CreditSettings, err error) {
	tx, err := o.s.beginCreditSettingsTx(ctx)
	if err != nil {
		return CreditSettings{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	out, err := setOperatorCreditSettingsTx(ctx, tx, up)
	if err != nil {
		return CreditSettings{}, err
	}
	return out, tx.Commit()
}

// WarmCPUClassCores returns the configured warm runner CPU class.
func (o *Operator) WarmCPUClassCores(ctx context.Context) (int64, error) {
	return o.s.creditSetting(ctx, metaKeyWarmCPUClass, DefaultWarmCPUClassCores)
}

// CreditRateTable returns the deployment price ladder.
func (o *Operator) CreditRateTable(ctx context.Context) (CreditRateTable, error) {
	settings, err := o.CreditSettings(ctx)
	return settings.RateTable, err
}

// GetSecretRow returns t's row stored under exactly this name, repository
// and pipeline, and ErrNotFound when there is none.
func (t *Tenant) GetSecretRow(name, repo, pipeline string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	rows, err := t.s.queryNoCtx(`SELECT `+secretCols+` FROM secrets WHERE team = ? AND name = ? AND repo = ? AND pipeline = ?`, string(t.team), name, repo, pipeline)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out, err := scanSecretRows(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, notFound("secret", name)
	}
	return &out[0], nil
}

// SetSignUpLimits replaces the thresholds. It leaves the mode and its
// provenance alone.
func (s *Store) SetSignUpLimits(ctx context.Context, limits SignUpLimits) (SignUpGate, error) {
	if limits.HourlyLimit < 0 || limits.DailyLimit < 0 || limits.HourlyWarn < 0 || limits.GitHubMinAccountDays < 0 ||
		limits.FreeTeamMembers < 0 {
		return SignUpGate{}, fmt.Errorf("%w: sign-up limits are zero or more", ErrInvalidInput)
	}
	return s.updateSignUpGate(ctx, func(tx *storeTx, g *SignUpGate) error {
		g.Limits = limits
		return nil
	})
}

// ApproveWaitlisted admits the named accounts: each leaves the waitlist and,
// if it belongs to no team, gets its personal space. An id that is not on the
// waitlist is skipped, so approving twice admits once. Each admission records
// an account.admitted business event naming actor, the operator's account, as
// the other break-glass methods do. It returns the accounts it admitted.
func (s *Store) ApproveWaitlisted(ctx context.Context, ids []string, actor string) ([]Account, error) {
	out := []Account{}
	for _, id := range ids {
		acct, ok, err := s.approveOne(ctx, id, actor)
		if err != nil {
			return out, err
		}
		if ok {
			out = append(out, acct)
		}
	}
	return out, nil
}

// SetSignUpMode records the operator's mode. Opening the gate also restarts
// the velocity windows, so the burst that closed it does not close it again
// on the next sign-up.
func (s *Store) SetSignUpMode(ctx context.Context, mode SignUpMode, reason, setBy string) (SignUpGate, error) {
	if _, err := ParseSignUpMode(string(mode)); err != nil {
		return SignUpGate{}, err
	}
	return s.updateSignUpGate(ctx, func(tx *storeTx, g *SignUpGate) error {
		g.Mode, g.Source, g.Reason, g.SetBy, g.UpdatedAt = mode, WaitlistReasonOperator, strings.TrimSpace(reason), setBy, tx.now(ctx)
		return nil
	})
}

// ApproveOldestWaitlisted admits the n accounts that have waited longest.
func (s *Store) ApproveOldestWaitlisted(ctx context.Context, n int, actor string) ([]Account, error) {
	if n <= 0 {
		return nil, fmt.Errorf("%w: approve at least one account", ErrInvalidInput)
	}
	waiting, err := s.WaitlistedAccounts(ctx, n)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(waiting))
	for _, w := range waiting {
		ids = append(ids, w.ID)
	}
	return s.ApproveWaitlisted(ctx, ids, actor)
}

// WaitlistedAccounts lists the accounts waiting for admission, oldest first.
// A limit of zero or less reads them all.
func (s *Store) WaitlistedAccounts(ctx context.Context, limit int) ([]WaitlistedAccount, error) {
	return s.waitlisted(ctx, "", limit, 0)
}

// RenewOwner renews owner-bound jobs and capacity within the owner lease transaction.
func (t *Tenant) RenewOwner(ctx context.Context, actor runstate.Actor) (runstate.LeaseBeat, error) {
	var beat runstate.LeaseBeat
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockActorTx(ctx, tx, actor); err != nil {
			return err
		}
		return t.renewOwnerTx(ctx, tx, actor, &beat)
	})
	return beat, err
}

// SetStorageSettings replaces the policy atomically. Invalid or sealed settings refuse the batch.
func (o *Operator) SetStorageSettings(ctx context.Context, in StorageSettings) error {
	return o.s.inTx(ctx, func(tx *storeTx) error { return setStorageSettingsTx(ctx, tx, in) })
}

// SetStorageAllowance changes retained bytes without replacing this team's write limits.
// Zero leaves retained bytes uncapped. The returned quota is from the write transaction.
func (t *Tenant) SetStorageAllowance(ctx context.Context, bytes int64) (StorageQuota, error) {
	var q StorageQuota
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		q, err = setStorageAllowanceTx(ctx, tx, t.team, bytes)
		return err
	})
	return q, err
}

// SetStorageQuota replaces this team's write limits and preserves its retained allowance.
// A named tier with zero write limits selects that tier's limits.
func (t *Tenant) SetStorageQuota(ctx context.Context, q StorageQuota) error {
	return t.s.inTx(ctx, func(tx *storeTx) error { return setStorageQuotaTx(ctx, tx, t.team, q) })
}

// ListTriggers returns this tenant's sealed trigger displays, newest first.
func (t *Tenant) ListTriggers(ctx context.Context, f TriggerFilter) (_ []*Trigger, err error) {
	if t.team == "" {
		return nil, ErrNoTeam
	}

	filter := RunFilter{Pipelines: f.Pipelines}
	if f.Repo != "" {
		filter.RepoNames = []string{f.Repo}
	}
	where, args, err := runFilterWhere(t.s.dialect, filter)
	if err != nil {
		return nil, err
	}
	if len(f.Statuses) > 0 {
		if where == "" {
			where = " WHERE "
		} else {
			where += " AND "
		}
		where += "triggers.status IN (" + placeholders(len(f.Statuses)) + ")"
		args = append(args, stringsToAny(f.Statuses)...)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	args = append(args, min(limit, MaxRunListLimit))
	args = append([]any{t.team}, args...)
	where = strings.Replace(where, " WHERE ", " AND ", 1)
	rows, err := t.s.query(ctx, triggerSelect+` WHERE runs.team = ?`+where+` ORDER BY triggers.created_at DESC,triggers.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []*Trigger
	for rows.Next() {
		trigger, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, trigger)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if err := t.s.refreshTriggerPresentations(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// CurrentSchemaVersion returns the schema version recorded in the
// database (MAX of sparkwing_schema_version), or 0 when unrecorded.
func (s *Store) CurrentSchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.queryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM sparkwing_schema_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// ListRuns returns runs ordered newest-first, then by id descending, filtered
// by f. The cursor clause depends on that order.
//
// Scope: this reads every team. Tenant callers use [Tenant.ListRuns].
func (o *Operator) ListRuns(ctx context.Context, f RunFilter) (_ []*Run, err error) {
	where, args, err := runFilterWhere(o.s.dialect, f)
	if err != nil {
		return nil, err
	}
	return o.s.listRuns(ctx, `SELECT `+runColumns+` FROM runs`+where, args, f)
}

// GetNode returns this tenant's job or ErrNotFound.
func (t *Tenant) GetNode(ctx context.Context, runID, nodeID string) (*Node, error) {
	row := t.s.queryRow(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ?`, string(t.team), runID, nodeID)
	record := &Node{}
	if err := scanNodeRow(row, record); err != nil {
		return nil, err
	}
	attempts, err := t.listNodeExecutionAttempts(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	record.ExecutionAttempts = attempts
	if err := t.populateNodeOutput(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

// CountRuns returns how many of t's runs match f, ignoring its Limit.
func (t *Tenant) CountRuns(ctx context.Context, f RunFilter) (int, error) {
	if t.team == "" {
		return 0, ErrNoTeam
	}
	where, args, err := runFilterWhere(t.s.dialect, f)
	if err != nil {
		return 0, err
	}
	where = strings.Replace(where, " WHERE ", " AND ", 1)
	row := t.s.queryRow(ctx, `SELECT COUNT(*) FROM runs WHERE team = ?`+where, append([]any{t.team}, args...)...)
	var count int
	err = row.Scan(&count)
	return count, err
}

func (t *Tenant) nodeIsAncestor(ctx context.Context, runID, ancestor, node string) (found bool, err error) {
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		dependencies, _, err := blobDependenciesTx(ctx, tx, t.team, runID, node)
		found = dependencies[ancestor]
		return err
	})
	return found, err
}

func (s *Store) creditSetting(ctx context.Context, key string, fallback int64) (int64, error) {
	raw, err := s.creditSettingRaw(ctx, key)
	if err != nil {
		return 0, err
	}
	return integerCreditSetting(key, raw, fallback)
}

func (s *Store) creditSettingRaw(ctx context.Context, key string) (string, error) {
	return scanCreditSetting(s.queryRow(ctx, "SELECT value,sealed FROM settings WHERE key=?", key), key)
}

// safety: SQLite's immediate transaction already serializes writers. The
// Postgres advisory lock gives its read-committed transaction the same single
// settings writer before an operator checks which rate representation owns the
// price.
func (s *Store) beginCreditSettingsTx(ctx context.Context) (*storeTx, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	if err := advisoryLockTx(ctx, tx, "sparkwing/credit-settings"); err != nil {
		rollbackOrLog(tx)
		return nil, err
	}
	return tx, nil
}

func (t *Tenant) requestTeamDeletionTx(ctx context.Context, tx *storeTx, now time.Time) (TeamDeletion, []string, error) {
	if err := acquireLockSet(ctx, tx, func() lockSet { return discoverTeamDeletionLocks(ctx, tx, lockSet{}, t.team) }); err != nil {
		return TeamDeletion{}, nil, err
	}
	revoked, err := t.markForDeletionTx(ctx, tx, "", now)
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	del, err := teamDeletionTx(ctx, tx, t.team)
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	return del, revoked, nil
}

func (s *Store) execNoCtx(q string, args ...any) (sql.Result, error) {
	return s.exec(context.Background(), q, args...)
}

func (s *Store) queryRowNoCtx(q string, args ...any) rowScanner {
	return s.queryRow(context.Background(), q, args...)
}

func (t *storeTx) query(q string, args ...any) (rowCursor, error) {
	return t.queryContext(context.Background(), q, args...)
}

func (t *storeTx) queryRow(q string, args ...any) rowScanner {
	return t.QueryRowContext(context.Background(), q, args...)
}

func (s *Store) eventLimits(ctx context.Context) (eventLimits, error) {
	return inTxResult(ctx, s, func(tx *storeTx) (eventLimits, error) {
		return eventLimitsTx(ctx, tx)
	})
}

// safety: zero lifts a cap.
func (s *Store) setEventLimits(ctx context.Context, limits eventLimits) error {
	return s.inTxOnce(ctx, func(tx *storeTx) error {
		if err := setCreditSettingTx(ctx, tx, metaKeyEventMaxBytes, formatCreditSetting(limits.MaxBytesPerEvent)); err != nil {
			return err
		}
		if err := setCreditSettingTx(ctx, tx, metaKeyEventMaxBytesPerRun, formatCreditSetting(limits.MaxBytesPerRun)); err != nil {
			return err
		}
		if err := setCreditSettingTx(ctx, tx, metaKeyEventMaxEventsPerRun, formatCreditSetting(limits.MaxEventsPerRun)); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) consumeFlowNonce(ctx context.Context, kind, nonce string, expires time.Time) (bool, error) {
	used := false
	err := s.inTx(ctx, func(tx *storeTx) error {
		var err error
		used, err = consumeFlowNonceTx(ctx, tx, kind, nonce, expires)
		return err
	})
	return err == nil && used, err
}

func (t *storeTx) forKeyShare() string {
	if t.dialect == DialectPostgres && !t.snapshotRead {
		return " FOR KEY SHARE"
	}
	return ""
}

// safety: these records never take a cascading run foreign key.
func financialTables() []string {
	return []string{
		"credit_grants", "credit_charges", "credit_checkouts", "credit_freezes",
		"billing_awards", "billable_intervals",
		"card_refunds", "payment_warnings", "payment_refunds", "payment_setups",
		"storage_run_usage", "storage_month_usage",
	}
}

func quotaPolicyTx(ctx context.Context, tx *storeTx, team Team, quota string, now time.Time) (string, int64, error) {
	switch quota {
	case "download":
		limit, err := numericSettingTx(ctx, tx, "operator.download_daily_max_bytes", 0)
		return now.UTC().Format("2006-01-02"), limit, err
	case "storage":
		var limit int64
		err := tx.QueryRowContext(ctx, `SELECT max_bytes_per_month FROM storage_quotas WHERE team = ?`, team).Scan(&limit)
		if errors.Is(err, sql.ErrNoRows) {
			err = fmt.Errorf("storage quota is not configured: %w", ErrNotFound)
		}
		if err == nil && limit < 0 {
			err = fmt.Errorf("storage quota must be nonnegative")
		}
		return now.UTC().Format("2006-01"), limit, err
	default:
		return "", 0, fmt.Errorf("unknown quota %q; supported quotas are download and storage", quota)
	}
}

func (t *Tenant) reserveQuota(ctx context.Context, quota string, amount int64) (QuotaReservation, error) {
	if amount < 0 {
		return QuotaReservation{}, fmt.Errorf("quota amount must be nonnegative")
	}
	return inTxResult(ctx, t.s, func(tx *storeTx) (QuotaReservation, error) {
		now := tx.now(ctx)
		period, ceiling, err := quotaPolicyTx(ctx, tx, t.team, quota, now)
		if err != nil {
			return QuotaReservation{}, err
		}
		if _, err := tx.execContext(ctx, `INSERT INTO quota_admission(team,quota,period,updated_at) VALUES (?,?,?,?) ON CONFLICT(team,quota) DO NOTHING`, t.team, quota, period, now.UnixNano()); err != nil {
			return QuotaReservation{}, err
		}
		var prior string
		var used, reserved int64
		if err := tx.QueryRowContext(ctx, `SELECT period,used,reserved FROM quota_admission WHERE team = ? AND quota = ?`+tx.forUpdate(), t.team, quota).Scan(&prior, &used, &reserved); err != nil {
			return QuotaReservation{}, err
		}
		now = tx.now(ctx)
		if quota == "download" {
			period = now.UTC().Format("2006-01-02")
		} else {
			period = now.UTC().Format("2006-01")
		}
		var expired int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(amount),0) FROM quota_reservations WHERE team = ? AND quota = ? AND period = ? AND state = 'held' AND expires_at <= ?`, t.team, quota, prior, now.UnixNano()).Scan(&expired); err != nil {
			return QuotaReservation{}, err
		}
		if _, err := tx.execContext(ctx, `UPDATE quota_reservations SET state = 'released' WHERE team = ? AND quota = ? AND state = 'held' AND expires_at <= ?`, t.team, quota, now.UnixNano()); err != nil {
			return QuotaReservation{}, err
		}
		reserved -= expired
		if prior != period {
			used, reserved = 0, 0
		}
		if reserved < 0 || used > math.MaxInt64-reserved || amount > math.MaxInt64-used-reserved {
			return QuotaReservation{}, fmt.Errorf("quota accounting overflow")
		}
		if ceiling > 0 && amount > ceiling-used-reserved {
			return QuotaReservation{}, &StorageQuotaError{Principal: string(t.team), Limit: quota, Unit: "bytes", Used: used + reserved, Allowed: ceiling, Requested: amount}
		}
		reservation := QuotaReservation{ID: "qr_" + rand.Text(), Quota: quota, Amount: amount, ExpiresAt: now.Add(time.Hour)}
		if _, err := tx.execContext(ctx, `UPDATE quota_admission SET period = ?, used = ?, reserved = ?, updated_at = ? WHERE team = ? AND quota = ?`, period, used, reserved+amount, now.UnixNano(), t.team, quota); err != nil {
			return QuotaReservation{}, err
		}
		if _, err := tx.execContext(ctx, `INSERT INTO quota_reservations(team,id,quota,period,amount,expires_at,state) VALUES (?,?,?,?,?,?,'held')`, t.team, reservation.ID, quota, period, amount, reservation.ExpiresAt.UnixNano()); err != nil {
			return QuotaReservation{}, err
		}
		return reservation, nil
	})
}

func (t *Tenant) settleQuota(ctx context.Context, id string, used int64) error {
	if id == "" || used < 0 {
		return fmt.Errorf("invalid quota settlement")
	}
	return t.s.inTxOnce(ctx, func(tx *storeTx) error {
		var quota string
		err := tx.QueryRowContext(ctx, `SELECT quota FROM quota_reservations WHERE team = ? AND id = ?`, t.team, id).Scan(&quota)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var currentPeriod string
		if err := tx.QueryRowContext(ctx, `SELECT period FROM quota_admission WHERE team = ? AND quota = ?`+tx.forUpdate(), t.team, quota).Scan(&currentPeriod); err != nil {
			return err
		}
		var state, period string
		var amount, expires, priorUsed int64
		if err := tx.QueryRowContext(ctx, `SELECT state,period,amount,expires_at,used FROM quota_reservations WHERE team = ? AND id = ?`+tx.forUpdate(), t.team, id).Scan(&state, &period, &amount, &expires, &priorUsed); err != nil {
			return err
		}
		if used > amount {
			return fmt.Errorf("quota settlement exceeds reservation")
		}
		if state != "held" {
			if priorUsed != used {
				return fmt.Errorf("conflicting quota settlement")
			}
			return nil
		}
		if used > 0 && expires <= tx.now(ctx).UnixNano() {
			return ErrLockHeld
		}
		if period == currentPeriod {
			if _, err := tx.execContext(ctx, `UPDATE quota_admission SET used = used + ?, reserved = reserved - ?, updated_at = ? WHERE team = ? AND quota = ?`, used, amount, tx.now(ctx).UnixNano(), t.team, quota); err != nil {
				return err
			}
		}
		state = "committed"
		if used == 0 {
			state = "released"
		}
		if _, err := tx.execContext(ctx, `UPDATE quota_reservations SET state = ?, used = ? WHERE team = ? AND id = ?`, state, used, t.team, id); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) updateSignUpGate(ctx context.Context, change func(*storeTx, *SignUpGate) error) (SignUpGate, error) {
	return inTxResult(ctx, s, func(tx *storeTx) (SignUpGate, error) {
		g, err := scanSignUpGate(tx.QueryRowContext(ctx, signUpGateSelect+tx.forUpdate()))
		if err != nil {
			return SignUpGate{}, err
		}
		if err := change(tx, &g); err != nil {
			return SignUpGate{}, err
		}
		if err := writeSignUpGateTx(ctx, tx, g); err != nil {
			return SignUpGate{}, err
		}
		return g, nil
	})
}

// safety: a personal slug can lose a race to a concurrent sign-in, and the
// retry re-reads what the winner took, as a first sign-in does.
func (s *Store) approveOne(ctx context.Context, id, actor string) (Account, bool, error) {
	var last error
	for range 5 {
		acct, ok, err := s.approveOneOnce(ctx, id, actor)
		if err == nil || (!isUniqueViolation(err) && !errors.Is(err, ErrSlugTaken)) {
			return acct, ok, err
		}
		last = err
	}
	return Account{}, false, last
}

func (s *Store) approveOneOnce(ctx context.Context, id, actor string) (Account, bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Account{}, false, err
	}
	defer rollbackOrLog(tx)
	acct, changed, err := tx.operator.approveOneTx(ctx, tx, id, auditAccountActor(actor), tx.now(ctx))
	if err != nil {
		return Account{}, false, err
	}
	return acct, changed, tx.Commit()
}

func (o *Operator) getLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration, fromAnyRun bool) (*Run, error) {
	return o.s.getLatestRun(ctx, `SELECT team,id FROM runs WHERE pipeline=?`, []any{pipeline}, pipeline, statuses, maxAge, fromAnyRun)
}

func isTerminalRunStatus(status string) bool {
	return status == "success" || status == runStatusFailed || status == runStatusCancelled
}

func schemaTableNames(owner schemaOwner) []string {
	var names []string
	for _, table := range schemaTables {
		if table.Owner == owner {
			names = append(names, table.Name)
		}
	}
	return names
}

// safety: another team's backlog is not counted, so it cannot shed t's submissions.
func (t *Tenant) countPendingTriggers(ctx context.Context) (int, error) {
	var n int
	if err := t.s.queryRow(ctx,
		`SELECT COUNT(*) FROM triggers WHERE team = ? AND status = ?`,
		string(t.team), triggerStatusPending).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

const (
	runStatusPending   = "pending"
	runStatusRunning   = "running"
	runStatusFailed    = "failed"
	runStatusCancelled = "cancelled"
	runStatusSuccess   = "success"
)

const (
	outcomeSuccess    = "success"
	outcomeFailed     = "failed"
	outcomeSkipped    = "skipped"
	outcomeCancelled  = "cancelled"
	outcomeSuperseded = "superseded"
)

const failureComputeLimit = "compute_limit"

var (
	tenantTables   = schemaTableNames(schemaTenant)
	operatorTables = schemaTableNames(schemaOperator)
)
