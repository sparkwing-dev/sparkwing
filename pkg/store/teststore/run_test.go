package teststore_test

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestJobReportingSeedUsesCurrentSchemaAndRejectsMissingRun(t *testing.T) {
	s, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := teststore.SeedUpload(t.Context(), s.DB()); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1700000000, 123).UTC()
	seed := store.Node{RunID: "upload", NodeID: "report", Status: "pending", Deps: []string{"previous"}, StartedAt: &started, AttemptsConsumed: 2}
	if err := teststore.SeedNode(s, t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.GetNode(t.Context(), seed.RunID, seed.NodeID)
	if err != nil || row.Status != seed.Status || row.StartedAt == nil || !row.StartedAt.Equal(started) || !reflect.DeepEqual(row.Deps, seed.Deps) || row.AttemptsConsumed != 2 {
		t.Fatalf("reporting fixture: %+v %v", row, err)
	}
	if err := teststore.SeedNodeReady(s, t.Context(), seed.RunID, seed.NodeID); err != nil {
		t.Fatal(err)
	}
	first, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: seed.RunID, NodeID: seed.NodeID})
	if err != nil || first.ReadyAt == nil {
		t.Fatalf("ready seed: %+v %v", first, err)
	}
	if err := teststore.SeedNodeReady(s, t.Context(), seed.RunID, seed.NodeID); err != nil {
		t.Fatal(err)
	}
	after, err := tenant.ReadNode(t.Context(), first.Ref)
	if err != nil || !after.ReadyAt.Equal(*first.ReadyAt) {
		t.Fatalf("ready seed replay: %+v %v", after, err)
	}
	seed.RunID = "missing"
	if err := teststore.SeedNode(s, t.Context(), seed); err == nil {
		t.Fatal("missing run silently seeded a job")
	}
	if err := teststore.SeedNodeReady(s, t.Context(), "missing", seed.NodeID); err == nil {
		t.Fatal("missing job silently became ready")
	}
}
