package launcher

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// The kubelet sends SIGTERM and only reports the Pod gone once its container
// exits, so a placeholder that ignores SIGTERM stalls every cold start.
func TestCapacityCommandExitsPromptlyOnSIGTERM(t *testing.T) {
	for _, path := range []string{capacityCommand[0], "/usr/bin/sleep"} {
		if _, err := os.Stat(path); err != nil {
			t.Skipf("%s is not available: %v", path, err)
		}
	}
	cmd := exec.Command(capacityCommand[0], append(capacityCommand[1:], "5")...) // #nosec G204 -- the launcher's fixed capacity script
	// PID 1 of a new PID namespace, as in the container: the kernel drops signals
	// PID 1 has no handler for, and its exit takes the namespace's sleep with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		Pdeathsig:   syscall.SIGKILL,
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("unprivileged PID namespaces are unavailable: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("capacity script did not exit cleanly on SIGTERM: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("capacity script took %s to exit on SIGTERM", took)
	}
}
