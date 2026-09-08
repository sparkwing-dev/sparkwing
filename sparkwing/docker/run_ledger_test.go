package docker

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

func dockerReachable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}
}

func TestRun_RecordsWhileRunningAndClearsAfter(t *testing.T) {
	dockerReachable(t)
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_RUN_ID", "run-dtest")
	t.Setenv("SPARKWING_NODE_ID", "build")
	ledger := sessionledger.Open(paths.PathsAt(home).SessionLedgerDir())

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), RunOptions{Image: "busybox", Cmd: []string{"sleep", "3"}})
	}()

	var recorded sessionledger.Record
	deadline := time.Now().Add(20 * time.Second)
	for {
		recs, _ := ledger.List()
		if len(recs) == 1 {
			recorded = recs[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("docker.Run never wrote a ledger record while the container ran")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if recorded.Handle.Kind != "docker" || recorded.Handle.Container == "" {
		t.Fatalf("recorded handle = %+v, want a docker container", recorded.Handle)
	}
	if recorded.Run != "run-dtest" || recorded.Node != "build" {
		t.Fatalf("record run/node = %s/%s", recorded.Run, recorded.Node)
	}

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if recs, _ := ledger.List(); len(recs) != 0 {
		t.Fatalf("ledger still holds a record after Run returned: %+v", recs)
	}
	// the container it created must carry the run label
	name := recorded.Handle.Container
	if out, err := exec.Command("docker", "inspect", "-f", "{{index .Config.Labels \"sparkwing.run\"}}", name).CombinedOutput(); err == nil {
		t.Fatalf("container %s still exists after Run (label=%s); it should have been removed", name, out)
	}
}

func TestRun_OutsideARunWritesNoRecord(t *testing.T) {
	dockerReachable(t)
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_RUN_ID", "")
	t.Setenv("SPARKWING_NODE_ID", "")
	if err := Run(context.Background(), RunOptions{Image: "busybox", Cmd: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if recs, _ := sessionledger.Open(paths.PathsAt(home).SessionLedgerDir()).List(); len(recs) != 0 {
		t.Fatalf("a run outside a pipeline wrote a ledger record: %+v", recs)
	}
}
