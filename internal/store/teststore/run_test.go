package teststore_test

import (
	"crypto/sha256"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestJobReportingSeedUsesCurrentSchemaAndRejectsMissingRun(t *testing.T) {
	now := time.Unix(1700000000, 123).UTC()
	s, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), teststore.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := teststore.SeedUpload(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte("upload-boundary"))); err != nil {
		t.Fatalf("publication authority differs from fixture clock: %v", err)
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
	first, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(seed.RunID), NodeID: runspec.NodeID(seed.NodeID)})
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
	seed.NodeID, seed.Status, seed.StartedAt, seed.AttemptsConsumed = "running", "running", nil, 0
	if err := teststore.SeedNode(s, t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	ref := runspec.NodeRef{RunID: runspec.RunID(seed.RunID), NodeID: runspec.NodeID(seed.NodeID)}
	running, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || running.Status != runstate.StatusRunning || running.StartedAt == nil || !running.StartedAt.Equal(now) || running.LeaseExpiresAt == nil || !running.LeaseExpiresAt.Equal(now.Add(time.Hour)) || running.Attempt != 1 || running.ClaimedBy == "" || running.CapabilityPrefix == "" || running.Generation != 1 {
		t.Fatalf("running fixture: %+v %v", running, err)
	}
	if err := teststore.SeedNodeFinish(s, t.Context(), store.DefaultTeam, ref, runstate.NodeResult{Outcome: runstate.NodeSuccess}); err != nil {
		t.Fatal(err)
	}
	done, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || done.Status != runstate.StatusDone || done.ClaimedBy != "" || done.CapabilityPrefix != "" || done.LeaseExpiresAt != nil || done.FinishedAt == nil {
		t.Fatalf("finished fixture: %+v %v", done, err)
	}
	seed.RunID = "missing"
	if err := teststore.SeedNode(s, t.Context(), seed); err == nil {
		t.Fatal("missing run silently seeded a job")
	}
	if err := teststore.SeedNodeReady(s, t.Context(), "missing", seed.NodeID); err == nil {
		t.Fatal("missing job silently became ready")
	}
}

func TestRunReportingSeedUsesSealedSchemaWithoutExecutableWork(t *testing.T) {
	s, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	at := time.Unix(1700000000, 123).UTC()
	seed := store.Run{
		ID: "report", Pipeline: "build", Status: "success", CreatedAt: at, StartedAt: at,
		Args: map[string]string{"target": "release"}, DeclaredRepo: "acme/widgets", RepoURL: "https://github.com/acme/widgets",
		GitBranch: "main", GitSHA: "abc123", TriggerSource: "schedule", TriggerUser: "alice", Error: "fixture failure",
	}
	if err := teststore.SeedRun(s, t.Context(), store.DefaultTeam, seed); err != nil {
		t.Fatal(err)
	}
	tenant := teststore.ForTeam(t, s, store.DefaultTeam)
	got, err := tenant.GetRun(t.Context(), seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 1 || got.Pipeline != seed.Pipeline || got.Status != seed.Status || got.DeclaredRepo != seed.DeclaredRepo || got.RepoURL != seed.RepoURL || got.GitBranch != seed.GitBranch || got.GitSHA != seed.GitSHA || got.TriggerSource != seed.TriggerSource || got.TriggerUser != seed.TriggerUser || got.Error != seed.Error || !got.StartedAt.Equal(at) || got.FinishedAt == nil || !reflect.DeepEqual(got.Args, map[string]string{"target": "***"}) {
		t.Fatalf("reporting fixture: %+v", got)
	}
	for _, table := range []string{"triggers", "eval_units", "nodes", "capabilities"} {
		var count int
		if err := teststore.SQL(t, s).QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s has %d rows: %v", table, count, err)
		}
	}
	if err := teststore.SeedRun(s, t.Context(), store.DefaultTeam, seed); err == nil {
		t.Fatal("duplicate reporting seed accepted")
	}
	seed.ID = "foreign"
	if err := teststore.SeedRun(s, t.Context(), "missing", seed); err == nil {
		t.Fatal("unregistered tenant accepted")
	}
}
