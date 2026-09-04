package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func seedDispatchRun(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.CreateRun(context.Background(), store.Run{
		ID:        id,
		Pipeline:  "test",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
}

func TestDispatch_RoundTrip(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedDispatchRun(t, s, "run-1")

	in := store.NodeDispatch{
		RunID:            "run-1",
		NodeID:           "build",
		Seq:              0,
		DispatchedAt:     time.Unix(1700000000, 0),
		CodeVersion:      "abc123",
		EnvJSON:          []byte(`{"SPARKWING_RUN_ID":"run-1"}`),
		Workdir:          "/repo",
		InputEnvelope:    []byte(`{"version":1,"type_name":"*pkg.Job"}`),
		InputSizeBytes:   42,
		SecretRedactions: 2,
		RedactedKeys:     []byte(`["GITHUB_TOKEN"]`),
	}
	if err := s.WriteNodeDispatch(ctx, in); err != nil {
		t.Fatalf("WriteNodeDispatch: %v", err)
	}

	out, err := s.GetNodeDispatch(ctx, "run-1", "build", 0)
	if err != nil {
		t.Fatalf("GetNodeDispatch: %v", err)
	}
	if out.RunID != in.RunID || out.NodeID != in.NodeID || out.Seq != 0 {
		t.Fatalf("identity drift: got %+v want %+v", out, in)
	}
	if out.CodeVersion != in.CodeVersion {
		t.Fatalf("code_version: got %q want %q", out.CodeVersion, in.CodeVersion)
	}
	if string(out.EnvJSON) != string(in.EnvJSON) {
		t.Fatalf("env_json: got %s", string(out.EnvJSON))
	}
	if out.Workdir != in.Workdir {
		t.Fatalf("workdir: got %q", out.Workdir)
	}
	if string(out.InputEnvelope) != string(in.InputEnvelope) {
		t.Fatalf("envelope: got %s", string(out.InputEnvelope))
	}
	if out.SecretRedactions != 2 {
		t.Fatalf("redactions: got %d want 2", out.SecretRedactions)
	}
	if string(out.RedactedKeys) != string(in.RedactedKeys) {
		t.Fatalf("redacted_keys: got %s", string(out.RedactedKeys))
	}
}

func TestDispatch_AutoSeq(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedDispatchRun(t, s, "run-1")

	for range 3 {
		if err := s.WriteNodeDispatch(ctx, store.NodeDispatch{
			RunID:  "run-1",
			NodeID: "build",
			Seq:    -1,
		}); err != nil {
			t.Fatalf("WriteNodeDispatch: %v", err)
		}
	}
	rows, err := s.ListNodeDispatches(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	for i, r := range rows {
		if r.Seq != i {
			t.Fatalf("rows[%d].Seq = %d, want %d", i, r.Seq, i)
		}
	}
}

func TestDispatch_GetLatest(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedDispatchRun(t, s, "run-1")
	for i := range 4 {
		if err := s.WriteNodeDispatch(ctx, store.NodeDispatch{
			RunID:       "run-1",
			NodeID:      "build",
			Seq:         i,
			CodeVersion: "v" + strings.Repeat("x", i+1),
		}); err != nil {
			t.Fatalf("write seq %d: %v", i, err)
		}
	}
	got, err := s.GetNodeDispatch(ctx, "run-1", "build", -1)
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if got.Seq != 3 {
		t.Fatalf("Seq=%d want 3", got.Seq)
	}
}

func TestDispatch_NotFound(t *testing.T) {
	s := storetest.Open(t)
	_, err := s.GetNodeDispatch(context.Background(), "run-x", "node-x", 0)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDispatch_Cascade(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedDispatchRun(t, s, "run-1")
	if err := s.WriteNodeDispatch(ctx, store.NodeDispatch{
		RunID: "run-1", NodeID: "build", Seq: 0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s, `DELETE FROM runs WHERE id = ?`), "run-1"); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	rows, err := s.ListNodeDispatches(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected cascade delete, got %d rows", len(rows))
	}
}

func TestDispatch_TruncationCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedDispatchRun(t, s, "run-1")

	big := make([]byte, store.MaxNodeDispatchEnvelope+1024)
	for i := range big {
		big[i] = 'A'
	}
	if err := s.WriteNodeDispatch(ctx, store.NodeDispatch{
		RunID: "run-1", NodeID: "build", Seq: 0,
		InputEnvelope: big,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := s.GetNodeDispatch(ctx, "run-1", "build", 0)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(string(got.InputEnvelope), `"truncated":true`) {
		t.Fatalf("expected truncation stub, got: %s", string(got.InputEnvelope))
	}
	if got.InputSizeBytes != int64(len(big)) {
		t.Fatalf("input_size_bytes: got %d want %d", got.InputSizeBytes, len(big))
	}
}

func TestDispatch_RequiresIDs(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if err := s.WriteNodeDispatch(ctx, store.NodeDispatch{NodeID: "build"}); err == nil {
		t.Fatalf("expected error on missing run_id")
	}
	if err := s.WriteNodeDispatch(ctx, store.NodeDispatch{RunID: "run-1"}); err == nil {
		t.Fatalf("expected error on missing node_id")
	}
}

func TestDispatch_ReplayOfColumns(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s, `
		INSERT INTO runs (id, pipeline, status, started_at, replay_of_run_id, replay_of_node_id)
		VALUES (?, ?, ?, ?, ?, ?)
	`), "replay-1", "test", "running", time.Now().UnixNano(), "orig-run", "build"); err != nil {
		t.Fatalf("insert with replay_of: %v", err)
	}
	var rro, rno string
	if err := s.DB().QueryRowContext(ctx, storetest.Rebind(s, `
		SELECT replay_of_run_id, replay_of_node_id FROM runs WHERE id = ?
	`), "replay-1").Scan(&rro, &rno); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if rro != "orig-run" || rno != "build" {
		t.Fatalf("replay_of: got (%q,%q)", rro, rno)
	}
}
