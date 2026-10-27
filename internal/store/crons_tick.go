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

// cronTickWindow leaves scheduling overhead inside a one-minute timer interval.
const cronTickWindow = 55 * time.Second

// RunCronTick runs one schedule tick unless one already ran inside the current window. The window stamp is
// written only after fn succeeds, so a failed or cancelled tick is retried by the next offer.
func (o *Operator) RunCronTick(ctx context.Context, fn func(context.Context) error) (ran bool, err error) {
	if fn == nil {
		return false, errors.New("cron tick requires a function")
	}
	state, err := o.RestoreStatus(ctx)
	if err != nil {
		return false, err
	}
	if state.Pending {
		return false, ErrRestorePending
	}
	now, err := o.s.databaseNow(ctx)
	if err != nil {
		return false, err
	}
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
		if now.Sub(time.Unix(0, at)) < cronTickWindow {
			return false, nil
		}
	}
	defer func() {
		if p := recover(); p != nil {
			ran, err = true, fmt.Errorf("cron tick panicked: %v", p)
		}
	}()
	if err = fn(ctx); err != nil {
		return true, err
	}
	return true, o.s.inTx(ctx, func(tx *storeTx) error {
		_, err := tx.execContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, cronTickStamp, []byte(strconv.FormatInt(tx.now(ctx).UnixNano(), 10)))
		return err
	})
}
