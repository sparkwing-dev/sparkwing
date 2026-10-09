package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	sealChildEnv   = "SPARKWING_TEST_SEAL_CHILD"
	sealChildReady = "seal-child: five lines stored"
)

// TestLogSealRunnerChild is the runner half of the e2e below: a separate
// process that writes a node's log to its engine's loopback endpoint
// through the runner's own log sink, then either finishes or waits to be
// killed.
func TestLogSealRunnerChild(t *testing.T) {
	mode := os.Getenv(sealChildEnv)
	if mode == "" {
		t.Skip("runs only as the e2e's child process")
	}
	nlog, err := NewHTTPLogsWithToken(os.Getenv("SEAL_LOGS_URL"), nil, os.Getenv("SEAL_TOKEN"), nil).
		OpenNodeLog(context.Background(), "run-s", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: fmt.Sprintf("step %d", i)})
	}
	if mode == "killed" {
		if err := nlog.(interface{ FlushExecutionAttempt() error }).FlushExecutionAttempt(); err != nil {
			t.Fatal(err)
		}
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
	for _, tc := range []struct {
		mode, wantState string
	}{
		{mode: "clean", wantState: logseg.StateComplete},
		{mode: "killed", wantState: logseg.StateCutOff},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			ctx := context.Background()
			ctrl, tok := newSegmentController(t)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			sink := openNodeLogSink(ctx, client.NewWithToken(ctrl.url, nil, tok), "run-s", "a", logger)
			if sink == nil {
				t.Fatal("the controller issued no segment URLs")
			}
			url, stop, err := serveLoopbackLogs(sink, tok, logger)
			if err != nil {
				t.Fatal(err)
			}
			defer stop(ctx)

			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLogSealRunnerChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), sealChildEnv+"="+tc.mode, "SEAL_LOGS_URL="+url, "SEAL_TOKEN="+tok)
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
			sink.close(ctx, false)
			if err := ctrl.st.StartNode(ctx, "run-s", "a"); err != nil {
				t.Fatal(err)
			}
			if err := ctrl.st.FinishNode(ctx, "run-s", "a", "success", "", nil); err != nil {
				t.Fatal(err)
			}

			b := backend.NewStoreBackend(ctrl.st, PathsAt(t.TempDir()), client.NewWithToken(ctrl.url, nil, tok).NodeLogs())
			all, err := b.ListNodes(ctx, "run-s")
			if err != nil {
				t.Fatal(err)
			}
			var nodes []*store.Node
			for _, n := range all {
				if n.NodeID == "a" {
					nodes = append(nodes, n)
				}
			}
			if len(nodes) != 1 {
				t.Fatalf("nodes = %v, want node a", all)
			}
			got, err := backend.NodeLogCompleteness(ctx, b, "run-s", nodes[0], time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.wantState || got.Lines != 5 {
				t.Fatalf("verdict = %+v, want %s over 5 lines", got, tc.wantState)
			}

			var logsOut bytes.Buffer
			if err := writeLogsViaBackend(ctx, b, "run-s", nodes, LogsOpts{}, &logsOut); err != nil {
				t.Fatal(err)
			}
			text := logsOut.String()
			if !strings.Contains(text, "step 5") {
				t.Fatalf("runs logs lost the log itself:\n%s", text)
			}
			if tc.mode == "clean" && strings.Contains(text, "[logs") {
				t.Fatalf("a sealed log drew a completeness line:\n%s", text)
			}
			lastLine := strings.TrimSpace(text[strings.LastIndex(strings.TrimSuffix(text, "\n"), "\n")+1:])
			if tc.mode == "killed" && (!strings.HasPrefix(lastLine, "[logs cut off") || lastLine != got.SyntheticLine()) {
				t.Fatalf("last line = %q, want the cut-off line %q\n%s", lastLine, got.SyntheticLine(), text)
			}
		})
	}
}
