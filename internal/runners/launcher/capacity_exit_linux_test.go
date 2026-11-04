package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// The kubelet sends SIGTERM and only reports the Pod gone once its container
// exits, so a placeholder that ignores SIGTERM stalls every cold start.
func TestCapacityCommandExitsPromptlyOnSIGTERM(t *testing.T) {
	for _, path := range []string{capacityCommand[0], "/usr/bin/sleep"} {
		if _, err := os.Stat(path); err != nil {
			t.Skipf("%s is not available: %v", path, err)
		}
	}
	cmd := exec.Command(capacityCommand[0], append(capacityCommand[1:], "86400")...) // #nosec G204 -- the launcher's fixed capacity script
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
	// The script arms its trap before it forks the sleep, so once PID 1 has a
	// child its signal dispositions are final; no delay is guessed.
	children := fmt.Sprintf("/proc/%d/task/%d/children", cmd.Process.Pid, cmd.Process.Pid)
	for {
		forked, err := os.ReadFile(children)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(forked)) != "" {
			break
		}
		runtime.Gosched()
	}
	if !catchesSIGTERM(t, cmd.Process.Pid) {
		t.Fatal("capacity script runs as PID 1 without a SIGTERM handler, so the kernel drops the kubelet's SIGTERM")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("capacity script did not exit cleanly on SIGTERM: %v", err)
	}
}

func catchesSIGTERM(t *testing.T, pid int) bool {
	t.Helper()
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if mask, ok := strings.CutPrefix(line, "SigCgt:\t"); ok {
			caught, err := strconv.ParseUint(mask, 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			return caught&(1<<(uint(syscall.SIGTERM)-1)) != 0
		}
	}
	t.Fatal("no SigCgt in process status")
	return false
}
