package cluster

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// The pipeline child learns its mode from a file in HOME, because the
// supervisor passes it only an allowlisted environment.
const pipelineChildModeFile = "pipeline-child-mode"

// runPipelineChildForTest stands in for a team's pipeline binary under
// run-node. "released" writes the way every released SDK does, one
// unnumbered append per line, then exits 0; "killed" does the same and dies
// by a signal; "sealing" numbers its lines and seals them itself.
func runPipelineChildForTest(runID, nodeID string) int {
	mode, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), pipelineChildModeFile))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pipeline child: no mode:", err)
		return 2
	}
	capability, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "pipeline child: no capability:", err)
		return 2
	}
	capability = strings.TrimSpace(capability)
	base := os.Getenv("SPARKWING_LOGS_URL")
	ctx := store.WithExecutionAttemptOrdinal(context.Background(), 1)
	client := logs.NewClientWithToken(base, nil, capability)
	for i := 1; i <= 5; i++ {
		line := []byte(fmt.Sprintf("{\"msg\":\"step %d\"}\n", i))
		appendCtx := ctx
		if string(mode) == "sealing" {
			appendCtx = logs.WithAppendSequence(ctx, "child", int64(i))
		}
		if err := client.Append(appendCtx, runID, nodeID, line); err != nil {
			fmt.Fprintln(os.Stderr, "pipeline child: append:", err)
			return 1
		}
	}
	switch string(mode) {
	case "killed":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	case "sealing":
		if err := client.Seal(ctx, runID, nodeID, logs.Seal{Stream: "child", FinalSeq: 5, Lines: 5}); err != nil {
			fmt.Fprintln(os.Stderr, "pipeline child: seal:", err)
			return 1
		}
	}
	return 0
}

// A node an agent claims from its pool runs in a child pipeline binary built
// against the team's pinned SDK, which may predate log seals. The agent's
// own supervisor carries every log line the child writes, so it numbers and
// seals them, and a finished node reads complete whatever SDK wrote it.
func TestPooledNode_AgentSealsTheLogOfAPipelineChild(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns pipeline child processes")
	}
	for _, tc := range []struct{ mode, want string }{
		{"released", logs.StateComplete},
		{"sealing", logs.StateComplete},
		// Negative control: a child killed mid-node leaves no seal.
		{"killed", logs.StateCutOff},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("SPARKWING_HOME", filepath.Join(home, "sparkwing"))
			t.Setenv("SPARKWING_CACHE_URL", "")
			if err := os.WriteFile(filepath.Join(home, pipelineChildModeFile), []byte(tc.mode), 0o600); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			ctrlSrv := httptest.NewServer(controller.New(st, nil).Handler())
			t.Cleanup(ctrlSrv.Close)
			logSrv, err := logs.New(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var seals []string
			logHandler := logSrv.Handler()
			logsHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/seal") {
					seals = append(seals, r.URL.Path)
				}
				logHandler.ServeHTTP(w, r)
			}))
			t.Cleanup(logsHTTP.Close)
			ctrl := client.New(ctrlSrv.URL, nil)
			ctx := context.Background()

			if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			if err := st.MarkNodeReady(ctx, "r1", "n1"); err != nil {
				t.Fatal(err)
			}
			claimed, err := ctrl.ClaimNode(ctx, "pool:1", nil, 3*time.Minute, nil)
			if err != nil || claimed == nil {
				t.Fatalf("claim node: %+v, %v", claimed, err)
			}

			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			executePooledNode(ctx, ctrl, ctrlSrv.URL, logsHTTP.URL, "", sourceurl.RepoAllowlist{}, "", claimed, claimed.ClaimedBy,
				3*time.Minute, time.Hour, "pool runner", logger, nil, nil)

			if _, err := st.DB().ExecContext(ctx,
				`UPDATE nodes SET status = 'done', outcome = 'success', started_at = ?, finished_at = ? WHERE run_id = 'r1' AND node_id = 'n1'`,
				time.Now().Add(-time.Hour).UnixNano(), time.Now().Add(-2*logs.SealGrace).UnixNano()); err != nil {
				t.Fatal(err)
			}
			b := backend.NewClientBackend(ctrl, sparkwinglogs.New(logsHTTP.URL, nil, ""))
			node, err := st.GetNode(ctx, "r1", "n1")
			if err != nil {
				t.Fatal(err)
			}
			got, err := backend.NodeLogCompleteness(ctx, b, "r1", node, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.want || got.Lines != 5 {
				t.Fatalf("verdict = %+v, want %s over 5 lines", got, tc.want)
			}
			if tc.mode == "sealing" && len(seals) != 1 {
				t.Fatalf("a child that seals itself was sealed %d times: %v", len(seals), seals)
			}
		})
	}
}
