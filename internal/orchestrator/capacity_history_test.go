package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRecordRunProfileCannotRelearnPreUpgradeExitUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	start := time.Unix(100, 0)
	if err := st.CreateRun(t.Context(), store.Run{ID: "old", Pipeline: "history", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(t.Context(), store.Node{RunID: "old", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(t.Context(), "old", "build", "success", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeUsage(t.Context(), "old", "build", store.NodeUsage{CPUTime: time.Second, Wall: 2 * time.Second, MaxRSSBytes: 512}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DELETE FROM sparkwing_schema_version WHERE version >= 51`, `DELETE FROM sparkwing_requirements WHERE name = 'process-tree-accounting'`} {
		if _, err := st.DB().ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	recordRunProfile(t.Context(), localState{st: st}, "history", "old", nil, "", runCharge{}, false, start, start.Add(2*time.Second))
	for _, node := range []string{"", "build"} {
		profile, err := st.GetPipelineProfile(t.Context(), "history", node)
		if err != nil || (profile != nil && profile.SampleCount != 0) {
			t.Fatalf("historical exit usage relearned: %+v, %v", profile, err)
		}
	}
}
