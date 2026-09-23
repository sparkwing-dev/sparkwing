package controller_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type storagePassFixture struct {
	st   *store.Store
	srv  *controller.Server
	logs *syncBuffer
}

// safety: acme holds one run that finished past a thirty-day window and one
// inside it, and one invitation spent long ago, so a pass has something to
// release and something to keep.
func newStoragePass(t *testing.T, multiTeam bool) storagePassFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := st.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []struct {
		id  string
		age time.Duration
	}{{"r-old", 31 * 24 * time.Hour}, {"r-new", time.Hour}} {
		if err := acme.CreateRun(ctx, store.Run{ID: run.id, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendEventCharged(ctx, "team:acme", run.id, "", "k", []byte("x")); err != nil {
			t.Fatalf("append to %s: %v", run.id, err)
		}
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO storage_run_usage (principal, run_id, bytes, objects, updated_at) VALUES ('team:acme', ?, 10, 1, 0)
			 ON CONFLICT (principal, run_id) DO NOTHING`, run.id); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET status = 'success', finished_at = ? WHERE id = ?`,
			time.Now().Add(-run.age).UnixNano(), run.id); err != nil {
			t.Fatal(err)
		}
	}
	spent := time.Now().Add(-60 * 24 * time.Hour).Unix()
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO invitations
		(id, team, email, role, invited_by, created_at, expires_at) VALUES ('inv-old', 'acme', 'a@example.com', 'reader', 'owner', ?, ?)`,
		spent, spent); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	srv := controller.New(st, slog.New(slog.NewTextHandler(logs, nil)))
	if multiTeam {
		raw, pub := multiTeamLicense(t)
		srv = srv.WithLicense(license.Resolve(raw, pub, time.Now(), nil))
	}
	return storagePassFixture{st: st, srv: srv, logs: logs}
}

func TestAMultiTeamStoragePassSeedsRetentionAndReleasesOldRuns(t *testing.T) {
	f := newStoragePass(t, true)
	ctx := context.Background()
	for range 2 {
		f.srv.MaintainStorage(ctx)
	}
	settings, err := f.st.StorageSettings(ctx)
	if err != nil || settings.EventRetentionDays != controller.CloudRetentionDays {
		t.Fatalf("retention = %+v, %v; want the cloud default seeded", settings, err)
	}
	var invitations int
	if err := f.st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM invitations`).Scan(&invitations); err != nil || invitations != 0 {
		t.Fatalf("invitations = %d, %v; want the spent one pruned", invitations, err)
	}
	logs := f.logs.String()
	for _, want := range []string{
		`msg="released storage past retention" team=acme runs=1`,
		`msg="pruned spent identity rows" invitations=1`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("the pass logged no %q:\n%s", want, logs)
		}
	}
	if n := strings.Count(logs, "released storage past retention"); n != 1 {
		t.Errorf("two passes released storage %d times, want once", n)
	}
}

// Negative control: a single-team controller seeds no cloud retention, so the
// same old run keeps its bytes.
func TestASingleTeamControllerKeepsItsOperatorsRetention(t *testing.T) {
	f := newStoragePass(t, false)
	ctx := context.Background()
	f.srv.MaintainStorage(ctx)
	settings, err := f.st.StorageSettings(ctx)
	if err != nil || settings.EventRetentionDays != 0 {
		t.Fatalf("retention = %+v, %v; want nothing seeded", settings, err)
	}
	if strings.Contains(f.logs.String(), "released storage past retention") {
		t.Fatalf("a single-team pass released storage:\n%s", f.logs.String())
	}
}
