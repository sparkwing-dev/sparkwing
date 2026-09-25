package localws

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A local install has no teams concept and is configured with none. Its
// rows are written by statements that name no team, and the dashboard
// has to keep serving them after the tenant key lands.
func TestLocalwsServesRowsWrittenWithNoTeam(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: starts the local server; the fast class runs under -short")
	}

	home := t.TempDir()
	paths, err := localPaths(home)
	if err != nil {
		t.Fatalf("localPaths: %v", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("EnsureRoot: %v", err)
	}

	ctx := context.Background()
	seed, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open the local state db: %v", err)
	}
	// safety: a binary that never heard of a team names none, so the seed does not either.
	for _, q := range []string{
		`INSERT INTO runs (id, pipeline, status, started_at) VALUES ('local-run', 'build', 'success', 1)`,
		`INSERT INTO nodes (run_id, node_id, status) VALUES ('local-run', 'compile', 'completed')`,
	} {
		if _, err := seed.DB().ExecContext(ctx, q); err != nil {
			t.Fatalf("seed (%s): %v", q, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close the seeded store: %v", err)
	}

	// safety: Options names no team, because a local install is configured with none.
	addr := startLocalws(t, Options{Home: home})

	resp := mustGet(t, "http://"+addr+"/api/v1/runs")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/v1/runs status = %d, want 200", resp.StatusCode)
	}
	var listed struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(listed.Runs) != 1 || listed.Runs[0]["id"] != "local-run" {
		t.Fatalf("/api/v1/runs = %v, want only local-run", listed.Runs)
	}

	detail := mustGet(t, "http://"+addr+"/api/v1/runs/local-run?include=nodes")
	defer func() { _ = detail.Body.Close() }()
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("/api/v1/runs/local-run status = %d, want 200", detail.StatusCode)
	}
	var wrap struct {
		Run   map[string]any   `json:"run"`
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.NewDecoder(detail.Body).Decode(&wrap); err != nil {
		t.Fatalf("decode run detail: %v", err)
	}
	if wrap.Run["id"] != "local-run" {
		t.Errorf("run.id = %v, want local-run", wrap.Run["id"])
	}
	if len(wrap.Nodes) != 1 {
		t.Errorf("got %d nodes, want 1", len(wrap.Nodes))
	}

	// safety: the rows the local server serves are the default team's, because the
	// same install has to read identically once its callers are ported.
	after, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("reopen the local state db: %v", err)
	}
	defer func() { _ = after.Close() }()
	def, err := after.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	run, err := def.GetRun(ctx, "local-run")
	if err != nil {
		t.Fatalf("default team GetRun: %v", err)
	}
	if run.Pipeline != "build" || run.Status != "success" {
		t.Errorf("run = %+v, want the seeded build/success row", run)
	}
	teams, err := after.AsOperator().ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 || teams[0] != store.DefaultTeam {
		t.Errorf("a local install lists teams %v, want only %q", teams, store.DefaultTeam)
	}
}
