//go:build unix

package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func outputController(t *testing.T, st *store.Store) *Claim {
	t.Helper()
	mux := http.NewServeMux()
	output := func(r *http.Request) []byte {
		out, err := st.GetNodeOutput(r.Context(), r.PathValue("run"), r.PathValue("node"))
		if err != nil {
			t.Errorf("read output of %s: %v", r.PathValue("node"), err)
		}
		return out
	}
	mux.HandleFunc("GET /api/v1/runs/{run}/nodes/{node}", func(w http.ResponseWriter, r *http.Request) {
		row, err := st.GetNode(r.Context(), r.PathValue("run"), r.PathValue("node"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": row.Status, "outcome": row.Outcome})
	})
	mux.HandleFunc("GET /api/v1/runs/{run}/nodes/{node}/output", func(w http.ResponseWriter, r *http.Request) {
		out := output(r)
		sum := sha256.Sum256(out)
		_ = json.NewEncoder(w).Encode(store.OutputReadGrant{
			URL: "/objects/" + r.PathValue("run") + "/" + r.PathValue("node"), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(out)),
		})
	})
	mux.HandleFunc("GET /objects/{run}/{node}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(output(r)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	claim, err := NewClaim(srv.URL, store.ClaimTokenPrefix+"_fanout")
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

type hookLog struct {
	memLog
	hook func(msg string)
}

func (l hookLog) Emit(rec sparkwing.LogRecord) {
	l.memLog.Emit(rec)
	l.hook(rec.Msg)
}

func TestFanOutEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	bin := filepath.Join(t.TempDir(), "attemptfixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/attemptfixture").CombinedOutput(); err != nil {
		t.Fatalf("build the fixture: %v\n%s", err, out)
	}
	host := func(t *testing.T, runID, fanout string) (*store.Store, RunResult) {
		t.Helper()
		srv, err := Start(nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		st := openStore(t)
		logs := &memLogs{records: map[string][]sparkwing.LogRecord{}}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		res, err := Execute(ctx, Config{
			Session:  SessionConfig{Entry: pipelineentry.Binary(bin), Dir: t.TempDir(), Grace: 2 * time.Second, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}},
			Pipeline: "attempts", Args: map[string]string{"fanout": fanout},
			Run:   nodeproto.RunContext{RunID: runID},
			State: st, Logger: slog.New(slog.DiscardHandler), Heartbeat: time.Hour,
			Attempts: &Attempts{
				Routes: srv, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(), Claim: outputController(t, st),
				OpenLog: func(_ context.Context, _, node string) (NodeLog, error) {
					logs.mu.Lock()
					defer logs.mu.Unlock()
					logs.open++
					return hookLog{memLog{logs: logs, node: node}, func(msg string) {
						if strings.Contains(msg, "generator hangs") {
							cancel()
						}
					}}, nil
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if logs.open != 0 {
			t.Errorf("%d node logs left open", logs.open)
		}
		return st, res
	}
	wantRow := func(t *testing.T, st *store.Store, runID, id string, outcome sparkwing.Outcome, errHas string) {
		t.Helper()
		row, err := st.GetNode(t.Context(), runID, id)
		if err != nil {
			t.Fatalf("node %s: %v", id, err)
		}
		if row.Outcome != string(outcome) || !strings.Contains(row.Error, errHas) {
			t.Errorf("node %s outcome=%q error=%q, want %s containing %q", id, row.Outcome, row.Error, outcome, errHas)
		}
	}

	t.Run("FansOutAndRunsTheWaiterLast", func(t *testing.T) {
		st, res := host(t, "run-fanout", "linux,darwin,windows")
		if res.Status != "success" {
			t.Fatalf("status=%s err=%v nodes=%+v, want success", res.Status, res.Err, res.Nodes)
		}
		want := []string{"test-linux", "test-darwin", "test-windows"}
		for _, id := range want {
			wantRow(t, st, "run-fanout", id, sparkwing.Success, "")
		}
		if got := res.Order[len(res.Order)-1]; got != "summary" {
			t.Errorf("order = %v, want summary last", res.Order)
		}
		row, err := st.GetNode(t.Context(), "run-fanout", "summary")
		if err != nil || !slices.Equal(row.Deps, want) {
			t.Errorf("summary deps = %v (err %v), want the generated nodes", row.Deps, err)
		}
	})

	t.Run("AGeneratorErrorCancelsItsWaiter", func(t *testing.T) {
		st, res := host(t, "run-boom", "linux,boom")
		if res.Status != "failed" {
			t.Fatalf("status=%s err=%v, want failed", res.Status, res.Err)
		}
		wantRow(t, st, "run-boom", "targets", sparkwing.Success, "")
		wantRow(t, st, "run-boom", "summary", sparkwing.Cancelled, "no target boom")
		if _, err := st.GetNode(t.Context(), "run-boom", "test-linux"); err == nil {
			t.Error("a failed generator still added nodes")
		}
	})

	t.Run("CancellingDuringExpansionStopsTheRun", func(t *testing.T) {
		st, res := host(t, "run-hang", "hang")
		if res.Status == "success" {
			t.Fatalf("status=%s, want the cancelled run not to succeed", res.Status)
		}
		wantRow(t, st, "run-hang", "summary", sparkwing.Cancelled, "")
		if _, err := st.GetNode(t.Context(), "run-hang", "test-hang"); err == nil {
			t.Error("a cancelled generator still added nodes")
		}
	})
}
