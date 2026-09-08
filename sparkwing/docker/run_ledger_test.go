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
	// safety: these tests start a container from a registry image; skip
	// where a pull cannot complete (e.g. a locked credential helper) rather
	// than fail on an environment the code under test does not control.
	if err := exec.Command("docker", "pull", "busybox").Run(); err != nil {
		t.Skip("docker cannot pull busybox in this environment")
	}
}

func TestRun_RegistersADockerCleanupWhileRunningAndClearsAfter(t *testing.T) {
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

	var rec sessionledger.Record
	deadline := time.Now().Add(20 * time.Second)
	for {
		recs, _ := ledger.List()
		if len(recs) == 1 {
			rec = recs[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("docker.Run never registered a cleanup while the container ran")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if rec.Handle.Kind != "command" {
		t.Fatalf("handle kind = %q, want command", rec.Handle.Kind)
	}
	argv := rec.Handle.Argv
	if len(argv) != 4 || argv[0] != "docker" || argv[1] != "rm" || argv[2] != "-f" || argv[3] == "" {
		t.Fatalf("cleanup argv = %v, want docker rm -f <name>", argv)
	}
	if rec.Run != "run-dtest" || rec.Node != "build" {
		t.Fatalf("record run/node = %s/%s", rec.Run, rec.Node)
	}

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if recs, _ := ledger.List(); len(recs) != 0 {
		t.Fatalf("cleanup still registered after Run returned: %+v", recs)
	}
	name := argv[3]
	if err := exec.Command("docker", "inspect", name).Run(); err == nil {
		t.Fatalf("container %s still exists after Run; it should have been removed", name)
	}
}

func TestRun_OutsideARunRegistersNothing(t *testing.T) {
	dockerReachable(t)
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_RUN_ID", "")
	t.Setenv("SPARKWING_NODE_ID", "")
	if err := Run(context.Background(), RunOptions{Image: "busybox", Cmd: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if recs, _ := sessionledger.Open(paths.PathsAt(home).SessionLedgerDir()).List(); len(recs) != 0 {
		t.Fatalf("a run outside a pipeline registered a cleanup: %+v", recs)
	}
}
