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

// deploymentRecord is what a started controller leaves beside its state for
// `admin`, which runs in the same pod and must act on the same store
// without the operator repeating the controller's flags.
type deploymentRecord struct {
	Kind           deployment.Kind  `json:"kind"`
	Store          deployment.Store `json:"store"`
	CredentialsDir string           `json:"credentials_dir"`
}

func deploymentRecordPath(stateDB string) string {
	return filepath.Join(filepath.Dir(stateDB), "deployment.json")
}

func writeDeploymentRecord(stateDB string, cfg deployment.Config, credentialsDir string) error {
	if credentialsDir != "" {
		abs, err := filepath.Abs(credentialsDir)
		if err != nil {
			return err
		}
		credentialsDir = abs
	}
	body, err := json.Marshal(deploymentRecord{Kind: cfg.Kind(), Store: cfg.Store(), CredentialsDir: credentialsDir})
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

func readDeploymentRecord(stateDB string) (deploymentRecord, error) {
	var rec deploymentRecord
	path := deploymentRecordPath(stateDB)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return rec, fmt.Errorf("no controller has started here (%s is missing); run admin in the controller's pod, beside the controller it acts for", path)
	}
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		return rec, fmt.Errorf("deployment record %s: %w", path, err)
	}
	return rec, nil
}

// openDeclaredStore opens the store the controller that last started here
// declared, and refuses rather than create one.
func openDeclaredStore(ctx context.Context, stateDB string) (*store.Store, error) {
	rec, err := readDeploymentRecord(stateDB)
	if err != nil {
		return nil, err
	}
	if rec.Store != deployment.StorePostgres {
		return store.OpenExisting(ctx, store.DialectSQLite, stateDB)
	}
	creds, err := readCredentials(rec.CredentialsDir)
	if err != nil {
		return nil, err
	}
	if creds.PGURL == "" {
		return nil, fmt.Errorf("the controller declared --store=postgres, but %s holds no %s", rec.CredentialsDir, credPGURL)
	}
	return store.OpenExisting(ctx, store.DialectPostgres, creds.PGURL)
}

const (
	serveLeaseName  = "controller.serve"
	serveLeaseTTL   = 30 * time.Second
	serveLeaseRenew = serveLeaseTTL / 3
)

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
		case <-time.After(serveLeaseRenew):
		}
	}
}

// keepServeLease renews lease until ctx ends, and calls lost when the
// lease can no longer be renewed, so the controller stops serving.
func keepServeLease(ctx context.Context, st *store.Store, lease store.SingletonLease, lost func(error)) {
	ticker := time.NewTicker(serveLeaseRenew)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewed, err := st.AsOperator().RenewSingletonLease(ctx, lease, serveLeaseTTL)
			if ctx.Err() != nil {
				return
			}
			// safety: a failed renewal is retried while the lease still runs,
			// so one database blip does not stop a healthy controller.
			if err != nil && !errors.Is(err, store.ErrLockHeld) && time.Until(lease.ExpiresAt) > serveLeaseRenew {
				continue
			}
			if err != nil {
				lost(fmt.Errorf("serve lease lost: %w", err))
				return
			}
			lease = renewed
		}
	}
}
