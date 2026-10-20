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
		return store.OpenControllerPostgres(ctx, cfg.PGURL(), func(err error) {
			slog.Error("controller must fail-stop", "err", err)
			os.Exit(1)
		})
	}
	return store.Open(sqlitePath) //nolint:contextcheck // Store.Open owns its bounded migration context and has no caller-context variant.
}

// deploymentRecord is what a serving controller leaves beside its state for
// `admin`, which runs in the same pod and must act on the same store
// without the operator repeating the controller's flags. It is written once
// startup has succeeded, and it names where the controller's connection
// landed, so admin refuses a store reached at another target or created at
// another time, such as one a rotated pg-url points at.
type deploymentRecord struct {
	Kind           deployment.Kind  `json:"kind"`
	Store          deployment.Store `json:"store"`
	CredentialsDir string           `json:"credentials_dir"`
	Connection     store.Connection `json:"connection"`
}

func deploymentRecordPath(stateDB string) string {
	return filepath.Join(filepath.Dir(stateDB), "deployment.json")
}

func writeDeploymentRecord(ctx context.Context, stateDB string, cfg deployment.Config, credentialsDir string, st *store.Store) error {
	conn, err := st.Connection(ctx)
	if err != nil {
		return err
	}
	if credentialsDir != "" {
		if credentialsDir, err = filepath.Abs(credentialsDir); err != nil {
			return err
		}
	}
	body, err := json.Marshal(deploymentRecord{Kind: cfg.Kind(), Store: cfg.Store(), CredentialsDir: credentialsDir, Connection: conn})
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

// openDeclaredStore opens the store the serving controller recorded. It
// never creates one. Verification and subsequent mutations retain the same
// connection, so reconnecting cannot silently redirect an admin operation.
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
	if rec.Connection.Target == "" {
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
	conn, err := st.Connection(ctx)
	switch {
	case err != nil:
	case conn.Target != rec.Connection.Target:
		err = fmt.Errorf("admin reached %s, but the serving controller is connected to %s; the credentials changed since it started", conn.Target, rec.Connection.Target)
	case conn.Created != rec.Connection.Created:
		err = fmt.Errorf("the store at %s is not the one the serving controller recorded (baseline %d, recorded %d)", conn.Target, conn.Created, rec.Connection.Created)
	}
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	return st, nil
}
