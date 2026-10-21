package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

func usageStatement(counter string) (string, error) {
	var table, column string
	switch counter {
	case "egress_bytes":
		table, column = "egress_usage", "bytes"
	case "download_bytes":
		table, column = "team_download_day", "bytes"
	case "storage_bytes":
		table, column = "storage_month_usage", "bytes"
	case "storage_objects":
		table, column = "storage_month_usage", "objects"
	case "runs":
		return `INSERT INTO run_rate_usage(team,period,replica_id,principal,runs,updated_at) VALUES (?,?,?,'',?,?)
ON CONFLICT(team,period,replica_id,principal) DO UPDATE SET runs = run_rate_usage.runs + excluded.runs, updated_at = excluded.updated_at
WHERE run_rate_usage.runs <= 9223372036854775807 - excluded.runs`, nil
	default:
		return "", fmt.Errorf("unknown usage counter %q", counter)
	}
	return `INSERT INTO ` + table + `(team,period,replica_id,` + column + `,updated_at) VALUES (?,?,?,?,?)
ON CONFLICT(team,period,replica_id) DO UPDATE SET ` + column + ` = ` + table + `.` + column + ` + excluded.` + column + `, updated_at = excluded.updated_at
WHERE ` + table + `.` + column + ` <= 9223372036854775807 - excluded.` + column, nil
}

func numericSettingTx(ctx context.Context, tx *storeTx, key string, fallback int64) (int64, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	var value *int64
	if err := json.Unmarshal(raw, &value); err != nil || value == nil || *value < 0 {
		return 0, fmt.Errorf("setting %s requires a nonnegative integer", key)
	}
	return *value, nil
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
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return QuotaReservation{}, err
	}
	defer rollbackOrLog(tx)
	now, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return QuotaReservation{}, clockErr1
	}
	period, ceiling, err := quotaPolicyTx(ctx, tx, t.team, quota, now)
	if err != nil {
		return QuotaReservation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quota_admission(team,quota,period,updated_at) VALUES (?,?,?,?) ON CONFLICT(team,quota) DO NOTHING`, t.team, quota, period, now.UnixNano()); err != nil {
		return QuotaReservation{}, err
	}
	var prior string
	var used, reserved int64
	if err := tx.QueryRowContext(ctx, `SELECT period,used,reserved FROM quota_admission WHERE team = ? AND quota = ?`+tx.forUpdate(), t.team, quota).Scan(&prior, &used, &reserved); err != nil {
		return QuotaReservation{}, err
	}
	clockTime2, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return QuotaReservation{}, clockErr2
	}
	now = clockTime2
	if quota == "download" {
		period = now.UTC().Format("2006-01-02")
	} else {
		period = now.UTC().Format("2006-01")
	}
	var expired int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(amount),0) FROM quota_reservations WHERE team = ? AND quota = ? AND period = ? AND state = 'held' AND expires_at <= ?`, t.team, quota, prior, now.UnixNano()).Scan(&expired); err != nil {
		return QuotaReservation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quota_reservations SET state = 'released' WHERE team = ? AND quota = ? AND state = 'held' AND expires_at <= ?`, t.team, quota, now.UnixNano()); err != nil {
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
	if _, err := tx.ExecContext(ctx, `UPDATE quota_admission SET period = ?, used = ?, reserved = ?, updated_at = ? WHERE team = ? AND quota = ?`, period, used, reserved+amount, now.UnixNano(), t.team, quota); err != nil {
		return QuotaReservation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quota_reservations(team,id,quota,period,amount,expires_at,state) VALUES (?,?,?,?,?,?,'held')`, t.team, reservation.ID, quota, period, amount, reservation.ExpiresAt.UnixNano()); err != nil {
		return QuotaReservation{}, err
	}
	return reservation, tx.Commit()
}

func (t *Tenant) settleQuota(ctx context.Context, id string, used int64) error {
	if id == "" || used < 0 {
		return fmt.Errorf("invalid quota settlement")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	var quota string
	err = tx.QueryRowContext(ctx, `SELECT quota FROM quota_reservations WHERE team = ? AND id = ?`, t.team, id).Scan(&quota)
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
		return tx.Commit()
	}
	clockTime3, clockErr3 := tx.now(ctx)
	if clockErr3 != nil {
		return clockErr3
	}
	if used > 0 && expires <= clockTime3.UnixNano() {
		return ErrLockHeld
	}
	if period == currentPeriod {
		clockTime4, clockErr4 := tx.now(ctx)
		if clockErr4 != nil {
			return clockErr4
		}
		if _, err := tx.ExecContext(ctx, `UPDATE quota_admission SET used = used + ?, reserved = reserved - ?, updated_at = ? WHERE team = ? AND quota = ?`, used, amount, clockTime4.UnixNano(), t.team, quota); err != nil {
			return err
		}
	}
	state = "committed"
	if used == 0 {
		state = "released"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quota_reservations SET state = ?, used = ? WHERE team = ? AND id = ?`, state, used, t.team, id); err != nil {
		return err
	}
	return tx.Commit()
}
