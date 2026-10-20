package controller_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func storageRetentionServer(t *testing.T, multiTeam bool) (*store.Store, *controller.Server) {
	t.Helper()
	s, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	old := time.Now().Add(-120 * 24 * time.Hour).UnixNano()
	if _, err := teststore.SQL(t, s).Exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days) VALUES (?,'old',1,'fixture','failed',?,'fixture',1,'gh:1','host','execute',?,'fixture',?,?,'r30',30)`, store.DefaultTeam, []byte(`{}`), []byte(`[]`), old, old); err != nil {
		t.Fatal(err)
	}
	var cfg deployment.Config
	if multiTeam {
		cfg = deploymenttest.Team(t)
	}
	srv := controller.NewDeployment(s, cfg, nil)
	return s, srv
}

func TestAMultiTeamStoragePassSeedsRetentionAndReleasesOldRuns(t *testing.T) {
	s, srv := storageRetentionServer(t, true)
	for range 2 {
		srv.MaintainStorage(t.Context())
	}
	settings, err := s.AsOperator().StorageSettings(t.Context())
	if err != nil || settings.RunRetentionDays != 90 || settings.NodeMetricRetentionDays != 0 {
		t.Fatalf("cloud policy=%+v: %v", settings, err)
	}
	var count int
	if err := teststore.SQL(t, s).QueryRow("SELECT COUNT(*) FROM runs").Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired runs=%d: %v", count, err)
	}
}

func TestASingleTeamControllerKeepsItsOperatorsRetention(t *testing.T) {
	s, srv := storageRetentionServer(t, false)
	srv.MaintainStorage(t.Context())
	settings, err := s.AsOperator().StorageSettings(t.Context())
	if err != nil || settings.RunRetentionDays != 0 {
		t.Fatalf("self-hosted policy=%+v: %v", settings, err)
	}
	var count int
	if err := teststore.SQL(t, s).QueryRow("SELECT COUNT(*) FROM runs").Scan(&count); err != nil || count != 1 {
		t.Fatalf("retained runs=%d: %v", count, err)
	}
}

func TestStorageRetentionPassRequiresMaintenanceFence(t *testing.T) {
	s, srv := storageRetentionServer(t, false)
	if err := s.AsOperator().SetStorageSettings(t.Context(), store.StorageSettings{RunRetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AsOperator().AcquireSingletonLease(context.Background(), "maintenance", "other", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv.MaintainStorage(t.Context())
	var count int
	if err := teststore.SQL(t, s).QueryRow("SELECT COUNT(*) FROM runs").Scan(&count); err != nil || count != 1 {
		t.Fatalf("foreign fence deleted run=%d: %v", count, err)
	}
	if _, err := teststore.SQL(t, s).Exec("UPDATE leases SET until=0 WHERE name=? AND generation=?", lease.Name, lease.Generation); err != nil {
		t.Fatal(err)
	}
	srv.MaintainStorage(t.Context())
	if err := teststore.SQL(t, s).QueryRow("SELECT COUNT(*) FROM runs").Scan(&count); err != nil || count != 0 {
		t.Fatalf("new fence retained expired run=%d: %v", count, err)
	}
}
