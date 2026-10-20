package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type deploymentFlags struct {
	Kind, Store, ExternalURL, PaymentMode string
	LimitsCloud                           bool
}

func parseDeployment(f deploymentFlags, creds controllerCredentials, now time.Time) (deployment.Config, error) {
	key, err := license.EmbeddedKey()
	if err != nil {
		return deployment.Config{}, err
	}
	return deployment.Parse(deployment.Inputs{
		Kind: f.Kind, Store: f.Store, PublicOrigin: f.ExternalURL, PaymentMode: f.PaymentMode,
		LimitsCloud: f.LimitsCloud, PGURL: creds.PGURL, License: creds.License, LicenseKey: key, Now: now,
		SecretsKey: creds.SecretsKey, SecretsPreviousKey: creds.SecretsPreviousKey,
		OIDCKey: creds.OIDCKey, OIDCPublishedKey: creds.OIDCPublishedKey,
		WebhookSecret: bytesOrNil(creds.GitHubAppWebhookSecret), PreviousWebhookSecret: bytesOrNil(creds.GitHubAppPreviousHook),
	})
}

func bytesOrNil(s string) []byte {
	if s == "" {
		return nil
	}
	return []byte(s)
}

func openControllerStore(ctx context.Context, cfg deployment.Config, sqlitePath string) (*store.Store, error) {
	if cfg.Store() == deployment.StorePostgres {
		return store.OpenPostgres(ctx, cfg.PGURL())
	}
	return store.Open(sqlitePath) //nolint:contextcheck // Store.Open owns its bounded migration context and has no caller-context variant.
}

// deploymentRecord is what a serving controller leaves beside its state for
// `admin`, which runs in the same pod and must act on the same store
// without the operator repeating the controller's flags. It is written only
// once startup has succeeded and the serve lease is held, and it names the
// store by its identity, so admin cannot be pointed at another database by
// a failed start or a rotated pg-url.
type deploymentRecord struct {
	Kind           deployment.Kind  `json:"kind"`
	Store          deployment.Store `json:"store"`
	CredentialsDir string           `json:"credentials_dir"`
	StoreIdentity  string           `json:"store_identity"`
}

func deploymentRecordPath(stateDB string) string {
	return filepath.Join(filepath.Dir(stateDB), "deployment.json")
}

func writeDeploymentRecord(ctx context.Context, stateDB string, cfg deployment.Config, credentialsDir string, st *store.Store) error {
	identity, err := st.StoreIdentity(ctx)
	if err != nil {
		return err
	}
	if credentialsDir != "" {
		if credentialsDir, err = filepath.Abs(credentialsDir); err != nil {
			return err
		}
	}
	body, err := json.Marshal(deploymentRecord{Kind: cfg.Kind(), Store: cfg.Store(), CredentialsDir: credentialsDir, StoreIdentity: identity})
	if err != nil {
		return err
	}
	path := deploymentRecordPath(stateDB)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".deployment-*")
	if err != nil {
		return fmt.Errorf("write deployment record: %w", err)
	}
	_, werr := tmp.Write(append(body, '\n'))
	if err := errors.Join(werr, tmp.Chmod(0o600), tmp.Close()); err != nil {
		return errors.Join(fmt.Errorf("write deployment record: %w", err), os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return errors.Join(fmt.Errorf("write deployment record: %w", err), os.Remove(tmp.Name()))
	}
	return nil
}

// openDeclaredStore opens the store the serving controller recorded, refuses
// rather than create one, and refuses a store whose identity differs.
func openDeclaredStore(ctx context.Context, stateDB string) (*store.Store, error) {
	path := deploymentRecordPath(stateDB)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no controller is serving here (%s is missing); run admin in the controller's pod, beside the controller it acts for", path)
	}
	if err != nil {
		return nil, err
	}
	var rec deploymentRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, fmt.Errorf("deployment record %s: %w", path, err)
	}
	if rec.StoreIdentity == "" {
		return nil, fmt.Errorf("deployment record %s names no store", path)
	}
	dialect, target := store.DialectSQLite, stateDB
	if rec.Store == deployment.StorePostgres {
		creds, err := readCredentials(rec.CredentialsDir)
		if err != nil {
			return nil, err
		}
		dialect, target = store.DialectPostgres, creds.PGURL
	}
	st, err := store.OpenExisting(ctx, dialect, target)
	if err != nil {
		return nil, err
	}
	identity, err := st.StoreIdentity(ctx)
	if err == nil && identity != rec.StoreIdentity {
		err = fmt.Errorf("the store admin reached is %s, but the serving controller recorded %s; the credentials changed since it started", identity, rec.StoreIdentity)
	}
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	return st, nil
}

const (
	serveLeaseName = "controller.serve"
	serveLeaseTTL  = 30 * time.Second
)

var errServeLeaseExpired = errors.New("serve lease expired before it was renewed; another controller may now serve this store")

// safety: per-process state such as flood budgets, live logs and the
// filesystem signing key is right only while one controller serves a store,
// so a second one waits here instead of serving beside the first. Waiting,
// rather than failing, lets a replacement pod start while the old lease
// runs out.
func acquireServeLease(ctx context.Context, st *store.Store, holder string, logger *slog.Logger) (store.SingletonLease, error) {
	warned := false
	for {
		lease, err := st.AsOperator().AcquireSingletonLease(ctx, serveLeaseName, holder, serveLeaseTTL)
		if err == nil {
			return lease, nil
		}
		if !errors.Is(err, store.ErrLockHeld) {
			return store.SingletonLease{}, fmt.Errorf("serve lease: %w", err)
		}
		if !warned {
			logger.Warn("another controller serves this store; standing by until its lease ends", "lease", serveLeaseName)
			warned = true
		}
		select {
		case <-ctx.Done():
			return store.SingletonLease{}, ctx.Err()
		case <-time.After(serveLeaseTTL / 3):
		}
	}
}

// serveWhileLeased runs serve until it returns or serving authority ends.
// Authority runs on this process's own clock: it ends ttl after the start
// of the last renewal that succeeded, whether or not a later renewal has
// returned, and each renewal must finish inside the window that is left.
// held is when the lease was last acquired. It does not wait for a renewal
// that ignores its deadline, so serving ends on time even then.
func serveWhileLeased(ctx context.Context, held time.Time, ttl time.Duration, renew, serve func(context.Context) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	expiry := held.Add(ttl)
	watchdog := time.AfterFunc(time.Until(expiry), func() { cancel(errServeLeaseExpired) })
	defer watchdog.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(ttl / 3):
			}
			sent := time.Now()
			rctx, rcancel := context.WithDeadline(ctx, expiry)
			err := renew(rctx)
			rcancel()
			switch {
			case err == nil:
				expiry = sent.Add(ttl)
				watchdog.Reset(time.Until(expiry))
			case errors.Is(err, store.ErrLockHeld):
				cancel(fmt.Errorf("serve lease lost: %w", err))
				return
			}
		}
	}()
	err := serve(ctx)
	cancel(nil)
	if cause := context.Cause(ctx); err == nil && cause != nil && !errors.Is(cause, context.Canceled) {
		err = cause
	}
	return err
}
