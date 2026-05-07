package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// TestRunJobsReceipt_LocalEmitsJSON exercises the local-mode CLI
// path end-to-end: seed a finished run + node, invoke runJobsReceipt,
// confirm valid receipt JSON lands on stdout. Mirrors the no-test
// pattern of runJobsGet -- the verb is small but the contract (CLI
// = canonical receipt shape) is load-bearing for IMP-016 acceptance.
func TestRunJobsReceipt_LocalEmitsJSON(t *testing.T) {
	dir := t.TempDir()
	paths := orchestrator.PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Second)
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-cli-1", Pipeline: "deploy", Status: "running",
		StartedAt: start,
		Args:      map[string]string{"env": "prod"},
		GitSHA:    "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE runs SET status='success', finished_at=? WHERE id='run-cli-1'`,
		end.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-cli-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE nodes SET status='done', outcome='success', started_at=?, finished_at=?, output_json=? WHERE node_id='build'`,
		start.UnixNano(), end.UnixNano(), []byte(`{"img":"x"}`)); err != nil {
		t.Fatal(err)
	}
	st.Close()

	out := captureStdout(t, func() {
		if err := runJobsReceipt(ctx, paths, []string{"--run", "run-cli-1"}); err != nil {
			t.Fatalf("runJobsReceipt: %v", err)
		}
	})

	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("decode receipt: %v\n%s", err, out)
	}
	if rec["run_id"] != "run-cli-1" {
		t.Errorf("run_id = %v, want run-cli-1", rec["run_id"])
	}
	if !strings.HasPrefix(rec["receipt_sha"].(string), "sha256:") {
		t.Errorf("receipt_sha = %q, want sha256: prefix", rec["receipt_sha"])
	}
	if _, ok := rec["cost"].(map[string]any); !ok {
		t.Errorf("cost section missing")
	}
}

// TestRunJobsReceipt_RejectsBadOutput pins the explicit error path:
// receipts only support -o json today.
func TestRunJobsReceipt_RejectsBadOutput(t *testing.T) {
	dir := t.TempDir()
	paths := orchestrator.PathsAt(dir)
	err := runJobsReceipt(context.Background(), paths,
		[]string{"--run", "x", "--output", "table"})
	if err == nil || !strings.Contains(err.Error(), "only supports json") {
		t.Fatalf("want only-json error, got %v", err)
	}
}

// captureStdout swaps os.Stdout for a pipe so the verb's
// json.Encoder writes can be inspected. Restores on cleanup.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()
	fn()
	w.Close()
	return string(<-done)
}
