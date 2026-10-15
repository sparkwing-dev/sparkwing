package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
)

func runCronsPin(session *cronsSession, name, format string, pin bool) error {
	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, name)
	if err != nil {
		return err
	}
	if pin {
		if _, err := session.svc.Lock(ctx, sched.ID, cronsProver(false)); err != nil {
			return fmt.Errorf("schedules set --pin: %w", err)
		}
		return emitCronsRow(ctx, session, sched.ID, format, "pinned")
	}
	if _, err := session.svc.Unlock(ctx, sched.ID); err != nil {
		return fmt.Errorf("schedules set --unpin: %w", err)
	}
	return emitCronsRow(ctx, session, sched.ID, format, "unpinned")
}

func emitCronsRow(ctx context.Context, session *cronsSession, id, format, verb string) error {
	row, _, err := session.svc.Show(ctx, id, 1)
	if err != nil {
		return err
	}
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(row)
	case "plain":
		_, perr := fmt.Fprintln(os.Stdout, row.ID)
		return perr
	}
	fmt.Fprintf(os.Stdout, "%s %s (%s)\n", verb, row.Display, cronsLockWord(row))
	return nil
}

func cronsLockWord(row crons.Row) string {
	ref := row.Lock.ShortRef()
	if ref == "" {
		ref = "pinned"
	}
	switch row.Lock.State {
	case crons.LockFollows:
		return crons.LockFollows
	case crons.LockMissing:
		return crons.LockMissing
	case crons.LockAhead:
		return ref + " ahead"
	case crons.LockDirty:
		return ref + " dirty"
	}
	return ref
}
