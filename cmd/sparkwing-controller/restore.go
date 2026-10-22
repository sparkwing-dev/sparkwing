package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const restoreUsage = `usage: sparkwing-controller restore prepare|acknowledge [--credentials-dir DIR] [--credential-epoch-dir DIR] [--store sqlite|postgres]

Stop every controller before replacing the database and keep it stopped for
these operations. prepare rotates credential-epoch outside the database,
invalidating every old bearer and session, cancelling unfinished scheduled runs,
and holding new schedule admissions. acknowledge discards catch-up through that time
and lifts the hold. API/local schedules need fresh consent before they can fire again.
All operations open an existing database and refuse to create one.
Payment recovery is manual; follow the operator runbook kept outside this repository.`

func runRestore(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "prepare" && args[0] != "acknowledge" {
		fmt.Fprintln(stderr, restoreUsage)
		return errors.New("restore needs prepare or acknowledge")
	}
	verb := args[0]
	fs := flag.NewFlagSet("restore "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, restoreUsage) }
	dir := fs.String(credentials.FlagName, "", "directory holding the controller's credential files")
	credentialEpochDir := fs.String(epochDirectoryFlag, "", epochDirectoryHelp)
	kind := fs.String("store", "sqlite", "existing store: sqlite or postgres (requires pg-url credential)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *kind != "sqlite" && *kind != "postgres" {
		return errors.New("restore requires --store sqlite or postgres and no positional arguments")
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		return err
	}
	creds, err := readCredentials(*dir)
	if err != nil {
		return err
	}
	if *kind == "postgres" && creds.PGURL == "" || *kind == "sqlite" && creds.PGURL != "" {
		return errors.New("restore --store must agree with the pg-url credential")
	}
	lock, err := wingd.AcquireControllerLock(p.Root)
	if err != nil {
		return err
	}
	defer lock.Close()
	epochDir := credentialEpochDirectory(*credentialEpochDir, *dir, p.Root)
	epoch, err := credentials.LoadOrCreateEpoch(epochDir)
	if err != nil {
		return err
	}
	dialect, target := store.DialectSQLite, p.StateDB()
	if *kind == "postgres" {
		dialect, target = store.DialectPostgres, creds.PGURL
	}
	ctx := context.Background()
	st, err := store.OpenRestore(ctx, dialect, target, store.WithCredentialEpoch(epoch))
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.EnsureCredentialEpoch(ctx); err != nil {
		return err
	}
	op := st.AsOperator()
	switch verb {
	case "prepare":
		epoch, err := credentials.RotateEpoch(epochDir)
		if err != nil {
			return err
		}
		if err := op.PrepareRestore(ctx, epoch); err != nil {
			return err
		}
	case "acknowledge":
		if err := op.AcknowledgeRestore(ctx); err != nil {
			return err
		}
	}
	state, err := op.RestoreStatus(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(state)
}
