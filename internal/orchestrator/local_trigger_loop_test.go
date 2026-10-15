package orchestrator

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestExecLocalChildCarriesTheChildsStderrInItsError(t *testing.T) {
	shell := "/bin/sh"
	if runtime.GOOS == "windows" {
		var err error
		shell, err = exec.LookPath("bash")
		if err != nil {
			t.Fatal(err)
		}
	}
	err := execLocalChild(context.Background(), shell, t.TempDir(),
		[]string{"-c", "echo noise; echo 'wingd/client: daemon build differs from this client' >&2; exit 1"}, nil)
	if err == nil {
		t.Fatal("expected the child's failure")
	}
	if !strings.Contains(err.Error(), "exit status 1") || !strings.Contains(err.Error(), "daemon build differs") {
		t.Fatalf("error should carry the exit status and the child's stderr: %v", err)
	}
}

func TestStderrTailKeepsOnlyTheEnd(t *testing.T) {
	tail := &stderrTail{limit: 16}
	for i := 0; i < 10; i++ {
		fmt.Fprintf(tail, "line %d\n", i)
	}
	got := tail.lastLines(2)
	if got != "line 8 | line 9" {
		t.Fatalf("lastLines = %q", got)
	}
}
