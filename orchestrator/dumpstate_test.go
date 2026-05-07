package orchestrator_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

// TestDumpRunState_RoundTrip pins the bijection between SQLite
// run/node rows (orchestrator write surface) and the
// runs/<id>/state.ndjson dump that S3-only dashboards read back
// (LOCAL-011). A new exported store.Run / store.Node field added
// without a JSON tag would silently disappear from the dashboard's S3
// view -- no compile error, no integration failure. This test makes
// that drift loud.
//
// PlanSnapshot is the one intentional omission: it carries `json:"-"`
// because the snapshot blob is large and the dashboard doesn't render
// it. The fixture populates PlanSnapshot to keep the
// "every-field-set" assertion honest, then clears it on `want` before
// the round-trip diff.
func TestDumpRunState_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	const runID = "run-rt-1"
	const nodeID = "compile"

	if err := st.CreateRun(ctx, store.Run{
		ID:             runID,
		Pipeline:       "build",
		Status:         "running",
		TriggerSource:  "manual",
		GitBranch:      "main",
		GitSHA:         "deadbeefcafef00d",
		Args:           map[string]string{"target": "release"},
		PlanSnapshot:   []byte(`{"plan":"snapshot"}`),
		StartedAt:      time.Unix(1746335000, 1),
		ParentRunID:    "parent-run",
		Repo:           "my-app",
		RepoURL:        "https://github.com/example/my-app.git",
		GithubOwner:    "example",
		GithubRepo:     "my-app",
		RetryOf:        "prior-run",
		RetriedAs:      "next-run",
		RetrySource:    "manual",
		ReplayOfRunID:  "replay-src-run",
		ReplayOfNodeID: "replay-src-node",
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.FinishRun(ctx, runID, "succeeded", "non-fatal warning"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	if err := st.CreateNode(ctx, store.Node{
		RunID:       runID,
		NodeID:      nodeID,
		Status:      "pending",
		Deps:        []string{"setup"},
		NeedsLabels: []string{"linux", "amd64"},
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	// Populate the rest of the node columns directly: the dump-format
	// bijection cares about the fields, not the mutation sequence the
	// orchestrator happens to use to set them.
	if _, err := st.DB().ExecContext(ctx, `
UPDATE nodes SET
   status='done', outcome='success', error='warn',
   output_json=?, started_at=?, finished_at=?, ready_at=?,
   claimed_by='runner-7', lease_expires_at=?,
   status_detail='compiling',
   last_heartbeat=?, failure_reason='exit_nonzero', exit_code=?
 WHERE run_id=? AND node_id=?`,
		[]byte(`{"out":"ok"}`),
		time.Unix(1746335100, 0).UnixNano(),
		time.Unix(1746335200, 0).UnixNano(),
		time.Unix(1746335090, 0).UnixNano(),
		time.Unix(1746335300, 0).UnixNano(),
		time.Unix(1746335150, 0).UnixNano(),
		17,
		runID, nodeID,
	); err != nil {
		t.Fatalf("populate node row: %v", err)
	}

	wantRun, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	wantNodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(wantNodes) != 1 {
		t.Fatalf("ListNodes len = %d, want 1", len(wantNodes))
	}

	// Fixture-completeness gate: if a developer adds a new exported
	// field to Run / Node without updating this fixture, the assertion
	// trips before the round-trip even runs. Forces the question
	// "should this round-trip?" to be answered explicitly.
	assertAllExportedNonZero(t, "Run", *wantRun)
	assertAllExportedNonZero(t, "Node", *wantNodes[0])

	art, err := fs.NewArtifactStore(filepath.Join(dir, "art"))
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	if err := orchestrator.DumpRunState(ctx, st, runID, art); err != nil {
		t.Fatalf("DumpRunState: %v", err)
	}

	b := backend.NewS3Backend(art, nil)
	gotRun, err := b.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("S3Backend.GetRun: %v", err)
	}
	gotNodes, err := b.ListNodes(ctx, runID)
	if err != nil {
		t.Fatalf("S3Backend.ListNodes: %v", err)
	}

	// PlanSnapshot is intentionally json:"-" -- not part of the
	// round-trip contract. Clear it on want before the diff so the
	// comparison stays focused on fields that *should* survive.
	wantRun.PlanSnapshot = nil

	// JSON's RFC3339Nano time format preserves the instant but not the
	// Location pointer (DB read produces Local; JSON unmarshal produces
	// a fixed-offset *time.Location). Normalize both sides to UTC so
	// reflect.DeepEqual compares wall time, not Location identity.
	normalizeRunTimes(wantRun)
	normalizeRunTimes(gotRun)
	for _, n := range wantNodes {
		normalizeNodeTimes(n)
	}
	for _, n := range gotNodes {
		normalizeNodeTimes(n)
	}

	if !reflect.DeepEqual(wantRun, gotRun) {
		t.Errorf("Run round-trip mismatch.\nwant=%+v\n got=%+v", wantRun, gotRun)
	}
	if !reflect.DeepEqual(wantNodes, gotNodes) {
		t.Errorf("Nodes round-trip mismatch.\nwant=%+v\n got=%+v", wantNodes, gotNodes)
	}
}

func assertAllExportedNonZero(t *testing.T, label string, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	for i := range rv.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		if rv.Field(i).IsZero() {
			t.Errorf("%s.%s is the zero value; populate it in the fixture so the round-trip diff covers this field", label, f.Name)
		}
	}
}

func normalizeRunTimes(r *store.Run) {
	r.StartedAt = r.StartedAt.UTC()
	if r.FinishedAt != nil {
		t := r.FinishedAt.UTC()
		r.FinishedAt = &t
	}
}

func normalizeNodeTimes(n *store.Node) {
	if n.StartedAt != nil {
		t := n.StartedAt.UTC()
		n.StartedAt = &t
	}
	if n.FinishedAt != nil {
		t := n.FinishedAt.UTC()
		n.FinishedAt = &t
	}
	if n.ReadyAt != nil {
		t := n.ReadyAt.UTC()
		n.ReadyAt = &t
	}
	if n.LeaseExpiresAt != nil {
		t := n.LeaseExpiresAt.UTC()
		n.LeaseExpiresAt = &t
	}
	if n.LastHeartbeat != nil {
		t := n.LastHeartbeat.UTC()
		n.LastHeartbeat = &t
	}
}
