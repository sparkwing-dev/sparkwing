//go:build linux

package wingd

import (
	"bufio"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func startMeasuredTestProcess(t *testing.T, script string) (*exec.Cmd, io.Writer, *bufio.Reader, string) {
	t.Helper()
	cmd := procenv.ControlCommand(nil, "/bin/sh", "-c", script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	birth, err := procgroup.ProcessBirth(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, stdin, bufio.NewReader(stdout), birth
}

// A host-run command is a sibling of the SDK process, so only its registration puts its CPU in the job's usage.
func TestHostRunCommandCPUCountsTowardTheJob(t *testing.T) {
	d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
	d.procSampler = newProcSampler()
	held := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "measured", 1)))
	root, _, _, rootBirth := startMeasuredTestProcess(t, "read x")
	busy, start, finished, busyBirth := startMeasuredTestProcess(t, `read x; i=0; while [ $i -lt 400000 ]; do i=$((i+1)); done; echo done; read x`)
	admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests/"+held.ID+"/process", map[string]any{"pid": root.Process.Pid, "process_birth": rootBirth}))
	if response := admissionHTTPCall(t, d, http.MethodPost, "/requests/"+held.ID+"/command", map[string]any{"pid": busy.Process.Pid, "process_birth": "reused"}); response.Code != http.StatusForbidden {
		t.Fatalf("command with a reused PID accepted: %d", response.Code)
	}
	admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests/"+held.ID+"/command", map[string]any{"pid": busy.Process.Pid, "process_birth": busyBirth}))
	if ticket := d.admissionTickets[held.ID]; ticket.MeasurementPID != root.Process.Pid {
		t.Fatalf("command registration replaced the job root: %d", ticket.MeasurementPID)
	}

	roots := d.measurementSample()
	if len(roots) != 1 || roots[0].PID != root.Process.Pid || !slices.Equal(roots[0].Members, []int{busy.Process.Pid}) {
		t.Fatalf("measurement roots = %+v", roots)
	}
	d.sampleProcessUsage(roots)
	if _, err := io.WriteString(start, "go\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := finished.ReadString('\n'); err != nil || line != "done\n" {
		t.Fatalf("busy command = %q, %v", line, err)
	}
	d.sampleProcessUsage(d.measurementSample())
	usage := d.processUsage(root.Process.Pid)
	if usage == nil || !usage.CPUMeasured || usage.CurrentCPUCores < 0.05 {
		t.Fatalf("job usage left out the host-run command: %+v", usage)
	}

	_ = busy.Process.Kill()
	_ = busy.Wait()
	if roots := d.measurementSample(); len(roots) != 1 || len(roots[0].Members) != 0 {
		t.Fatalf("ended command still measured: %+v", roots)
	}
}
