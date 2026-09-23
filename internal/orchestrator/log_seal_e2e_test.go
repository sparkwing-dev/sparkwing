package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	sealChildEnv   = "SPARKWING_TEST_SEAL_CHILD"
	sealChildReady = "seal-child: five lines stored"
)

// TestLogSealRunnerChild is the runner half of the e2e below: a separate
// process that writes a node's log to the logs service through the
// runner's own log sink, then either finishes or waits to be killed.
func TestLogSealRunnerChild(t *testing.T) {
	mode := os.Getenv(sealChildEnv)
	if mode == "" {
		t.Skip("runs only as the e2e's child process")
	}
	nlog, err := NewHTTPLogs(os.Getenv("SEAL_LOGS_URL"), nil, nil).
		OpenNodeLog(context.Background(), os.Getenv("SEAL_RUN"), "build", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: fmt.Sprintf("step %d", i)})
	}
	if mode == "killed" {
		// Emit returns once the service has stored the line, so the parent
		// can kill this process the moment it reads the marker.
		fmt.Println(sealChildReady)
		select {}
	}
	if err := nlog.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLogSeal_E2E_RunnerChildSealsOrIsCutOff(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns runner child processes")
	}
	logSrv, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(logSrv.Handler())
	defer hs.Close()
	client := logs.NewClient(hs.URL, nil)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	b := backend.NewStoreBackend(st, PathsAt(dir), sparkwinglogs.New(hs.URL, nil, ""))

	for _, tc := range []struct {
		mode, run, wantState, wantLine string
	}{
		{mode: "clean", run: "run-clean", wantState: logs.StateComplete},
		{
			mode: "killed", run: "run-killed", wantState: logs.StateCutOff,
			wantLine: "— logs cut off: the log stream ended without the runner's confirmation after line 5 —",
		},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			ctx := context.Background()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLogSealRunnerChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), sealChildEnv+"="+tc.mode, "SEAL_LOGS_URL="+hs.URL, "SEAL_RUN="+tc.run)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ready := false
			sc := bufio.NewScanner(stdout)
			for !ready && sc.Scan() {
				ready = sc.Text() == sealChildReady
			}
			if tc.mode == "killed" {
				if !ready {
					t.Fatalf("runner child exited before storing its lines: %s", stderr.String())
				}
				_ = cmd.Process.Kill()
			}
			_, _ = io.Copy(io.Discard, stdout)
			if err := cmd.Wait(); err != nil && tc.mode == "clean" {
				t.Fatalf("runner child: %v\n%s", err, stderr.String())
			}

			seedFinishedNode(t, st, tc.run, "build", time.Now().Add(-2*logs.SealGrace))
			nodes, err := b.ListNodes(ctx, tc.run)
			if err != nil || len(nodes) != 1 {
				t.Fatalf("nodes = %v, %v", nodes, err)
			}
			got, err := backend.NodeLogCompleteness(ctx, b, tc.run, nodes[0], time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.wantState || got.Lines != 5 {
				t.Fatalf("verdict = %+v, want %s over 5 lines", got, tc.wantState)
			}

			var logsOut bytes.Buffer
			if err := writeLogsViaBackend(ctx, b, tc.run, nodes, LogsOpts{}, &logsOut); err != nil {
				t.Fatal(err)
			}
			text := logsOut.String()
			if !strings.Contains(text, "step 5") {
				t.Fatalf("runs logs lost the log itself:\n%s", text)
			}
			lastLine := strings.TrimSpace(text[strings.LastIndex(strings.TrimSuffix(text, "\n"), "\n")+1:])
			if tc.wantLine == "" && strings.Contains(text, "— logs") {
				t.Fatalf("a sealed log drew a completeness line:\n%s", text)
			}
			if tc.wantLine != "" && lastLine != tc.wantLine {
				t.Fatalf("last line = %q, want %q\n%s", lastLine, tc.wantLine, text)
			}
			stored, err := client.Read(ctx, tc.run, "build")
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(stored, []byte("logs cut off")) {
				t.Fatal("the synthetic line reached the stored log")
			}
		})
	}
}

func seedFinishedNode(t *testing.T, st *store.Store, runID, nodeID string, finishedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.StartNode(ctx, runID, nodeID); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(ctx, runID, nodeID, "success", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET finished_at = ? WHERE run_id = ? AND node_id = ?`,
		finishedAt.UnixNano(), runID, nodeID); err != nil {
		t.Fatal(err)
	}
}
