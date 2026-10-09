package cluster

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// hack: the mode travels in a file in HOME because the supervisor passes the child
// only an allowlisted environment.
const pipelineChildModeFile = "pipeline-child-mode"

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
	nodeURL := os.Getenv("SPARKWING_LOGS_URL") + "/api/v1/logs/" + runID + "/" + nodeID
	post := func(target string, body []byte, headers map[string]string) error {
		req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+capability)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return fmt.Errorf("%s answered HTTP %d", target, resp.StatusCode)
		}
		return nil
	}
	var descendant *exec.Cmd
	if string(mode) == "cancel" {
		descendant = exec.Command("sh", "-c", "exec sleep 300")
		if err := descendant.Start(); err != nil {
			return 2
		}
		pids := strconv.Itoa(os.Getpid()) + " " + strconv.Itoa(descendant.Process.Pid)
		if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "pipeline-child-pids"), []byte(pids), 0o600); err != nil {
			_ = descendant.Process.Kill()
			_ = descendant.Wait()
			return 2
		}
	}
	for i := 1; i <= 5; i++ {
		line := []byte(fmt.Sprintf("{\"msg\":\"step %d\"}\n", i))
		var headers map[string]string
		if string(mode) == "sealing" {
			headers = map[string]string{logseg.LogStreamHeader: "child", logseg.LogSeqHeader: strconv.Itoa(i)}
		}
		if err := post(nodeURL, line, headers); err != nil {
			fmt.Fprintln(os.Stderr, "pipeline child: append:", err)
			return 1
		}
	}
	switch string(mode) {
	case "cancel":
		_ = descendant.Wait()
	case "killed":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	case "sealing":
		seal, err := json.Marshal(logseg.WriterSeal{Stream: "child", FinalSeq: 5})
		if err != nil {
			return 2
		}
		if err := post(nodeURL+"/seal", seal, nil); err != nil {
			fmt.Fprintln(os.Stderr, "pipeline child: seal:", err)
			return 1
		}
	}
	return 0
}

// A node an agent claims from its pool runs in a child pipeline binary built
// against the team's pinned SDK, which may predate log seals. The agent's
// own supervisor keeps every line the child writes as segments through the
// controller and seals them, so a finished node reads complete whatever SDK
// wrote it, and a killed child's log reads cut off.
func TestPooledNode_AgentSealsTheLogOfAPipelineChild(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns pipeline child processes")
	}
	for _, tc := range []struct{ mode, want string }{
		{"released", logseg.StateComplete},
		{"sealing", logseg.StateComplete},
		{"killed", logseg.StateCutOff},
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
			executePooledNode(ctx, ctrl, ctrlSrv.URL, "", sourceurl.RepoAllowlist{}, "", claimed, claimed.ClaimedBy,
				3*time.Minute, time.Hour, "pool runner", logger, nil, nil)

			if _, err := st.DB().ExecContext(ctx,
				`UPDATE nodes SET status = 'done', outcome = 'success', started_at = ?, finished_at = ? WHERE run_id = 'r1' AND node_id = 'n1'`,
				time.Now().Add(-time.Hour).UnixNano(), time.Now().Add(-2*logseg.SealGrace).UnixNano()); err != nil {
				t.Fatal(err)
			}
			b := backend.NewClientBackend(ctrl, ctrl.NodeLogs())
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
		})
	}
}
