package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const cronTickStamp = "crons.tick"

// CronTickWindow leaves scheduling overhead inside a one-minute timer interval.
const CronTickWindow = 55 * time.Second

// RunCronTickLeased runs one schedule tick under the deployment's fenced lease.
func (o *Operator) RunCronTickLeased(ctx context.Context, holder string, ttl time.Duration, fn func(context.Context) error) (ran bool, err error) {
	if fn == nil || holder == "" {
		return false, errors.New("cron tick requires a holder and function")
	}
	if ttl <= 0 {
		ttl = CronTickWindow
	}
	identity, err := newOpaqueIdentity(holder)
	if err != nil {
		return false, err
	}
	lease, err := o.AcquireSingletonLease(ctx, "schedules.tick", identity, ttl)
	if errors.Is(err, ErrLockHeld) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("cron tick for %s panicked: %v", holder, p)
		}
		err = errors.Join(err, o.ReleaseSingletonLease(context.WithoutCancel(ctx), lease))
	}()
	var last []byte
	err = o.s.queryRow(ctx, `SELECT value FROM settings WHERE key=?`, cronTickStamp).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil {
		at, parseErr := strconv.ParseInt(string(last), 10, 64)
		if parseErr != nil {
			return false, parseErr
		}
		if o.s.now().Sub(time.Unix(0, at)) < CronTickWindow {
			return false, nil
		}
	}
	ran = true
	if err = fn(ctx); err != nil {
		return true, err
	}
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		if err := assertSingletonLeaseTx(ctx, tx, lease, "schedules.tick", tx.now()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, cronTickStamp, []byte(strconv.FormatInt(tx.now().UnixNano(), 10)))
		return err
	})
	return true, err
}
