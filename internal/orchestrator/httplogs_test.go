package orchestrator_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type httpLogsPipe struct{ sparkwing.Base }

func (httpLogsPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	a := sparkwing.Job(plan, "a", func(ctx context.Context) error {
		sparkwing.Info(ctx, "a: first line")
		sparkwing.Info(ctx, "a: second line")
		return nil
	})
	sparkwing.Job(plan, "b", func(ctx context.Context) error {
		sparkwing.Info(ctx, "b: only line")
		return nil
	}).Needs(a)
	return nil
}

// Each node's lines reach its own append path on the engine's log
// endpoint, and each node seals what it sent.
func TestHTTPLogs_PipelineLogsReachTheEndpoint(t *testing.T) {
	register("httplogs-demo", func() sparkwing.Pipeline[sparkwing.NoInputs] { return httpLogsPipe{} })

	var mu sync.Mutex
	got := map[string]*bytes.Buffer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if got[r.URL.Path] == nil {
			got[r.URL.Path] = &bytes.Buffer{}
		}
		got[r.URL.Path].Write(body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	paths := orchestrator.PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}

	local := orchestrator.LocalBackends(paths, st, nil)
	backends := orchestrator.Backends{
		State:       local.State,
		Logs:        orchestrator.NewHTTPLogs(srv.URL, nil, nil),
		Concurrency: local.Concurrency,
	}

	res, err := orchestrator.Run(context.Background(), backends,
		orchestrator.Options{Pipeline: "httplogs-demo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status=%q want success", res.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	base := "/api/v1/logs/" + res.RunID + "/"
	for node, wants := range map[string][]string{
		"a": {"a: first line", "a: second line"},
		"b": {"b: only line"},
	} {
		body := got[base+node]
		if body == nil {
			t.Fatalf("node %s sent no log; endpoint received %v", node, slices.Collect(maps.Keys(got)))
		}
		for _, want := range wants {
			if !strings.Contains(body.String(), want) {
				t.Errorf("node %s log missing %q:\n%s", node, want, body)
			}
		}
		seal := got[base+node+"/seal"]
		if want := fmt.Sprintf(`"final_seq":%d,"dropped":0`, strings.Count(body.String(), "\n")); seal == nil || !strings.Contains(seal.String(), want) {
			t.Errorf("node %s seal = %v, want %s", node, seal, want)
		}
	}
}
