//go:build linux

package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

const shepherdTestMode = "SPARKWING_SHEPHERD_TEST_MODE"

// safety: fixtures: "shepherd" runs ShepherdTrigger over "host", and "host" starts a node process that
// leads its own session and has a descendant in another process group, records both, and kills itself, as
// a trigger host killed right after it starts a node would.
func TestShepherdFixture(t *testing.T) {
	pids := os.Getenv("SPARKWING_SHEPHERD_TEST_PIDS")
	switch os.Getenv(shepherdTestMode) {
	case "shepherd":
		_ = os.Setenv(shepherdTestMode, "host")
		if ok, _ := ShepherdTrigger([]string{os.Args[0], "-test.run=^TestShepherdFixture$"}); !ok {
			t.Fatal("no child subreaper on this kernel")
		}
	case "host":
		node := exec.Command("sh", "-c", `set -m; sleep 300 & echo $!; wait`)
		out, err := node.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := procgroup.StartSession(node); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(out).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pids, fmt.Appendf(nil, "%d %s", node.Process.Pid, line), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	default:
		t.Skip("fixture")
	}
}

func TestAShepherdReapsWhatAKilledTriggerHostLeft(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShepherdFixture$")
	cmd.Env = append(os.Environ(), shepherdTestMode+"=shepherd", "SPARKWING_SHEPHERD_TEST_PIDS="+pids)
	_ = cmd.Run()
	raw, err := os.ReadFile(pids)
	if err != nil {
		t.Fatalf("the host never started its node: %v", err)
	}
	processes, err := procgroup.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range strings.Fields(string(raw)) {
		for _, p := range processes {
			if fmt.Sprint(p.PID) == field && !p.Terminated() {
				_ = exec.Command("kill", "-9", field).Run()
				t.Fatalf("process %s the killed host left outlived its shepherd", field)
			}
		}
	}
}
