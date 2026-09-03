package orchestrator_test

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDumpRunState_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

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
		Invocation: map[string]any{
			"binary_source": "cached",
			"reproducer":    "sparkwing run build --target=release",
		},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.FinishRun(ctx, runID, "succeeded", "non-fatal warning"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if err := st.TouchRunHeartbeat(ctx, runID); err != nil {
		t.Fatalf("TouchRunHeartbeat: %v", err)
	}
	if _, err := st.DB().ExecContext(
		ctx, `
UPDATE runs SET annotation_count = ?, top_annotation = ?, annotations_json = ?
 WHERE id = ?`,
		2, "linked in 1.2s",
		[]byte(`["compiled 14 MiB","linked in 1.2s"]`),
		runID,
	); err != nil {
		t.Fatalf("populate run annotation rollup: %v", err)
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
	if _, err := st.DB().ExecContext(
		ctx, `
UPDATE nodes SET
   status='done', outcome='success', error='warn',
   output_json=?, started_at=?, finished_at=?, ready_at=?,
   claimed_by='runner-7', lease_expires_at=?,
   status_detail='compiling',
   last_heartbeat=?, failure_reason='exit_nonzero', exit_code=?,
   annotations_json=?, summary=?, artifact_manifest='sha-cafef00d',
   cpu_nanos=?, max_rss_bytes=?, process_wall_nanos=?
 WHERE run_id=? AND node_id=?`,
		[]byte(`{"out":"ok"}`),
		time.Unix(1746335100, 0).UnixNano(),
		time.Unix(1746335200, 0).UnixNano(),
		time.Unix(1746335090, 0).UnixNano(),
		time.Unix(1746335300, 0).UnixNano(),
		time.Unix(1746335150, 0).UnixNano(),
		17,
		[]byte(`["compiled 14 MiB","linked in 1.2s"]`),
		"## compile\n- 14 MiB binary",
		int64(4200*time.Millisecond),
		int64(384<<20),
		int64(5100*time.Millisecond),
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
	wantNodes[0] = api.PublicNode(wantNodes[0])

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

	wantRun.PlanSnapshot = nil

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

func TestDumpRunState_RedactsClaimIdentityAndRetainsAttemptLineage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	const runID = "run-dump-redaction"
	const nodeID = "build"
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "build", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixNano()
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET
 claimed_by='private-holder', claim_worker_id='desktop-public', claim_executor_kind='agent',
 claim_reservation_id='private-claim-reservation', coordinator_id='private-coordinator',
 claim_generation=7, claim_membership_id='private-membership', executor_kind='agent',
 executor_id='private-executor', executor_location='local',
 required_coordinator_id='private-required-coordinator', required_executor_location='local',
 execution_started_at=?, reservation_id='private-reservation', attempts_consumed=2,
 retry_root_run_id='run-lineage-root', lease_expires_at=?
 WHERE run_id=? AND node_id=?`, now, now+int64(time.Minute), runID, nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO node_execution_attempts
 (lineage_root_run_id, run_id, node_id, attempt_ordinal, claim_generation,
  coordinator_id, membership_id, executor_kind, executor_name, executor_id, executor_location,
  holder_id, reservation_id, started_at, finished_at, outcome, failure_reason, retry_run_id)
 VALUES ('run-lineage-root', ?, ?, 2, 7,
  'private-attempt-coordinator', 'private-attempt-membership', 'agent', 'desktop-public',
  'private-attempt-executor', 'local', 'private-attempt-holder', 'private-attempt-reservation',
  ?, ?, 'failed', 'agent_lost', 'run-retry-public')`, runID, nodeID, now, now+1); err != nil {
		t.Fatal(err)
	}

	art, err := fs.NewArtifactStore(filepath.Join(dir, "art"))
	if err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.DumpRunState(ctx, st, runID, art); err != nil {
		t.Fatal(err)
	}
	r, err := art.Get(ctx, "runs/"+runID+"/state.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	dump := string(raw)
	for _, banned := range []string{
		`"claimed_by"`, `"claim_worker_id"`, `"claim_executor_kind"`, `"claim_reservation_id"`,
		`"coordinator_id"`, `"claim_generation"`, `"claim_membership_id"`, `"executor_id"`,
		`"required_coordinator_id"`, `"reservation_id"`,
		"private-holder", "private-claim-reservation", "private-coordinator", "private-membership",
		"private-executor", "private-required-coordinator", "private-reservation",
		"private-attempt-coordinator", "private-attempt-membership", "private-attempt-executor",
		"private-attempt-holder", "private-attempt-reservation",
	} {
		if strings.Contains(dump, banned) {
			t.Errorf("state dump contains private claim identity %q:\n%s", banned, dump)
		}
	}
	for _, retained := range []string{
		`"claimed":true`, `"executor_kind":"agent"`, `"executor_name":"desktop-public"`,
		`"executor_location":"local"`, `"required_executor_location":"local"`,
		`"run_id":"run-dump-redaction"`, `"attempt":2`,
		`"outcome":"failed"`, `"failure_reason":"agent_lost"`, `"retry_run_id":"run-retry-public"`,
	} {
		if !strings.Contains(dump, retained) {
			t.Errorf("state dump does not retain public execution field %q:\n%s", retained, dump)
		}
	}

	reader := backend.NewS3Backend(art, nil)
	nodes, err := reader.ListNodes(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || len(nodes[0].ExecutionAttempts) != 1 {
		t.Fatalf("dump nodes = %+v", nodes)
	}
	got := nodes[0]
	if got.ExecutorName != "desktop-public" || !got.Claimed || got.ClaimedBy != "" || got.ExecutorID != "" || got.CoordinatorID != "" {
		t.Errorf("public node attribution = %+v", got)
	}
	attempt := got.ExecutionAttempts[0]
	if attempt.RunID != runID || attempt.NodeID != nodeID || attempt.RetryRunID != "run-retry-public" ||
		attempt.Attempt != 2 || attempt.ExecutorKind != "agent" || attempt.ExecutorName != "desktop-public" ||
		attempt.ExecutorLocation != "local" || attempt.Outcome != "failed" ||
		attempt.FailureReason != store.FailureAgentLost || attempt.StartedAt.IsZero() ||
		attempt.FinishedAt == nil || attempt.ClaimGeneration != 0 {
		t.Errorf("attempt lineage = %+v", attempt)
	}
}

func normalizeRunTimes(r *store.Run) {
	r.CreatedAt = r.CreatedAt.UTC()
	r.StartedAt = r.StartedAt.UTC()
	if r.FinishedAt != nil {
		t := r.FinishedAt.UTC()
		r.FinishedAt = &t
	}
	if r.LastHeartbeatAt != nil {
		t := r.LastHeartbeatAt.UTC()
		r.LastHeartbeatAt = &t
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
