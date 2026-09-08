//go:build unix

package sparkwing

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestExec_OwnsTheCommandSessionOnlyWhileItRuns(t *testing.T) {
	if len(procgroup.Owned()) != 0 {
		t.Fatalf("owned groups before the command: %v", procgroup.Owned())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = execCmd(ctx, "sleep", []string{"60"}, t.TempDir(), nil)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for len(procgroup.Owned()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("a running command was never owned")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled command did not return")
	}
	if got := procgroup.Owned(); len(got) != 0 {
		t.Fatalf("owned groups after the command returned: %v", got)
	}
}
