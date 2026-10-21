package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const restoreUsage = `usage: sparkwing-controller restore prepare|reconcile|acknowledge [--credentials-dir DIR] [--credential-epoch-dir DIR] [--store sqlite|postgres]

Stop every controller before replacing the database and keep it stopped for
these operations. prepare rotates credential-epoch outside the database,
invalidating every old bearer and session, and holds schedules. reconcile
pulls provider facts through normal payment settlement. acknowledge discards
schedule catch-up through the acknowledgement time and resumes future ticks.
All operations open an existing database and refuse to create one.
For reconcile notifications, use the serving controller's --email-sender,
--email-configuration-set and --dashboard-url. Without a sender, mail is logged.`

func runRestore(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "prepare" && args[0] != "reconcile" && args[0] != "acknowledge" {
		fmt.Fprintln(stderr, restoreUsage)
		return errors.New("restore needs prepare, reconcile or acknowledge")
	}
	verb := args[0]
	fs := flag.NewFlagSet("restore "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, restoreUsage) }
	dir := fs.String(credentials.FlagName, "", "directory holding the controller's credential files")
	credentialEpochDir := fs.String(epochDirectoryFlag, "", epochDirectoryHelp)
	kind := fs.String("store", "sqlite", "existing store: sqlite or postgres (requires pg-url credential)")
	emailSender := fs.String("email-sender", "", "SES sender for recovery notifications; use the serving controller's value")
	emailConfigSet := fs.String("email-configuration-set", "", "SES configuration set for recovery notifications")
	dashboardURL := fs.String("dashboard-url", "", "dashboard URL used in recovery notification links")
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
	state, err := op.RestoreStatus(ctx)
	if err != nil {
		return err
	}
	switch verb {
	case "prepare":
		epoch, err := credentials.RotateEpoch(epochDir)
		if err != nil {
			return err
		}
		if err := op.PrepareRestore(ctx, epoch); err != nil {
			return err
		}
	case "reconcile":
		mode, err := op.RestorePaymentMode(ctx)
		if err != nil {
			return err
		}
		verifier, err := readPaymentAuthenticator(*dir, string(mode))
		if err != nil {
			return err
		}
		if err := op.ReconcileRestore(ctx, func(ctx context.Context) error {
			if mode == "" {
				return nil
			}
			deployment, err := op.DeploymentID(ctx)
			if err != nil {
				return err
			}
			reporter := controller.New(st, slog.New(slog.NewTextHandler(stderr, nil))).
				WithPayments(verifier, op).
				WithPaymentDeployment(deployment).
				WithDashboardURL(*dashboardURL)
			if err := configureMailer(ctx, reporter, *emailSender, *emailConfigSet); err != nil {
				return err
			}
			return reporter.ReconcilePayments(ctx)
		}); err != nil {
			return err
		}
	case "acknowledge":
		if err := op.AcknowledgeRestore(ctx); err != nil {
			return err
		}
	}
	state, err = op.RestoreStatus(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(state)
}
