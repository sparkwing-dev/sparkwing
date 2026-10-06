//go:build !windows

package cluster

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/testhome"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPooledNode_CancelStopsPipelineDescendants(t *testing.T) {
	testPooledNodeStop(t, false)
}

func TestAgent_SIGTERMStopsPipelineDescendants(t *testing.T) {
	testPooledNodeStop(t, true)
}

func testPooledNodeStop(t *testing.T, terminateAgent bool) {
	t.Helper()
	home := t.TempDir()
	testhome.Set(t, home)
	t.Setenv("SPARKWING_HOME", filepath.Join(home, "sparkwing"))
	t.Setenv("SPARKWING_CACHE_URL", "")
	if err := os.WriteFile(filepath.Join(home, pipelineChildModeFile), []byte("cancel"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctrlHTTP := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(ctrlHTTP.Close)
	ctrl := client.New(ctrlHTTP.URL, nil)
	logSrv, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	var once sync.Once
	logHandler := logSrv.Handler()
	logsHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logHandler.ServeHTTP(w, r)
		if r.Method == http.MethodPost {
			once.Do(func() { close(started) })
		}
	}))
	t.Cleanup(logsHTTP.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "r1", Pipeline: "p", CreatedAt: time.Now(), TriggerSource: "runs-submit"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "r1", "n1"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var agent *procgroup.Group
	if terminateAgent {
		config := fmt.Sprintf("agent:\n  controller: %s\n  logs: %s\n  token: test-token\n  local_admission: false\n  heartbeat: 200ms\n  poll: 20ms\n", ctrlHTTP.URL, logsHTTP.URL)
		configPath := filepath.Join(home, "config.yaml")
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		agent, err = procgroup.StartSession(exec.Command(executable, "agent", "--config", configPath))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = agent.Terminate(context.Background(), time.Second) })
		go func() {
			_ = agent.Finish(context.Background(), time.Second)
			close(done)
		}()
	} else {
		claimed, err := ctrl.ClaimNode(ctx, "pool:1", nil, time.Minute, nil)
		if err != nil || claimed == nil {
			t.Fatalf("claim = %+v, %v", claimed, err)
		}
		go func() {
			defer close(done)
			executePooledNode(ctx, ctrl, ctrlHTTP.URL, logsHTTP.URL, "", sourceurl.RepoAllowlist{}, "", claimed, claimed.ClaimedBy,
				time.Minute, 200*time.Millisecond, "agent", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
		}()
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("pipeline child did not start")
	}
	pids, err := os.ReadFile(filepath.Join(home, "pipeline-child-pids"))
	if err != nil {
		t.Fatal(err)
	}
	var parentPID, descendantPID int
	if _, err := fmt.Sscan(string(pids), &parentPID, &descendantPID); err != nil {
		t.Fatal(err)
	}
	identity, err := procgroup.CaptureSession(parentPID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = procgroup.TerminateSession(identity) })
	if terminateAgent {
		if err := syscall.Kill(agent.ID(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := ctrl.CancelRun(ctx, "r1"); err != nil {
			t.Fatal(err)
		}
		if err := ctrl.FinishRun(ctx, "r1", "cancelled", "cancel requested"); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("cancelled node kept its pipeline process %d and descendant %d", parentPID, descendantPID)
	}
	if empty, err := procgroup.SessionEmpty(identity); err != nil || !empty {
		t.Fatalf("cancelled pipeline session = empty %t, %v", empty, err)
	}
	node, err := ctrl.GetNode(ctx, "r1", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if terminateAgent {
		if node.Status == "done" || node.Outcome != "" {
			t.Fatalf("agent shutdown finished a node instead of allowing lease recovery: %+v", node)
		}
	} else if node.Status != "done" || node.Outcome != "cancelled" {
		t.Fatalf("cancelled pipeline node = %+v, %v", node, err)
	}
}
