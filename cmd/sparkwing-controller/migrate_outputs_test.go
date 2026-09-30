package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestMigrateOutputsMovesInlineOutputsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	unsetenv(t, controllerPostgresEnv)
	ctx := context.Background()
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "run-inline", Pipeline: "p", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-inline", NodeID: "n", Status: "done", Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE nodes SET output_json = ? WHERE run_id = 'run-inline'`, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runMigrateOutputs(nil, &out); err != nil {
		t.Fatalf("migrate-outputs: %v", err)
	}
	if !strings.Contains(out.String(), "done: 1 outputs") {
		t.Fatalf("migrate-outputs said %q, want one output moved", out.String())
	}
	out.Reset()
	if err := runMigrateOutputs(nil, &out); err != nil || !strings.Contains(out.String(), "done: 0 outputs") {
		t.Fatalf("a second run: %q, %v, want nothing moved", out.String(), err)
	}
	st, err = store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	if got, err := st.GetNodeOutput(ctx, "run-inline", "n"); err != nil || string(got) != `{"v":1}` {
		t.Fatalf("output after the move = %s, %v", got, err)
	}
}
