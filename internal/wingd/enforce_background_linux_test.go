//go:build linux

package wingd

import (
	"os/exec"
	"testing"
)

func TestBackgroundProcessRenicesLinuxHolder(t *testing.T) {
	child := exec.Command("cat")
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Skipf("cat is unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = child.Wait()
	})
	if err := backgroundProcess(child.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if nice, ok := processNice("/", child.Process.Pid); !ok || nice != backgroundNice {
		t.Fatalf("holder nice = %d (read %v); want %d", nice, ok, backgroundNice)
	}
}
