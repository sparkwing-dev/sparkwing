//go:build unix

package local

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestClassifyExit_UnpromptedSIGKILLNamesTheNodeAndItsPeakRSS(t *testing.T) {
	usage := &runner.ResourceUsage{MaxRSSBytes: 3 << 30}
	v := classifyExit("build", exitInfo{signaled: true, signal: syscall.SIGKILL, code: -1}, false, usage)

	if v.result.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %q, want failed", v.result.Outcome)
	}
	for _, want := range []string{"build", "SIGKILL", "out-of-memory", "3.0GiB"} {
		if !strings.Contains(v.message, want) {
			t.Errorf("message %q does not mention %q", v.message, want)
		}
	}

	if v.reason != store.FailureUnknown {
		t.Errorf("reason = %q, want %q", v.reason, store.FailureUnknown)
	}
	if v.exitCode != nil {
		t.Errorf("exitCode = %v, want nil for a signaled process", *v.exitCode)
	}
}

func TestClassifyExit_SIGKILLWithoutUsageOmitsThePeak(t *testing.T) {
	v := classifyExit("build", exitInfo{signaled: true, signal: syscall.SIGKILL}, false, nil)
	if strings.Contains(v.message, "peak RSS") {
		t.Errorf("message %q claims a peak it does not have", v.message)
	}
}

func TestClassifyExit_CancelledIsCancelledNotFailed(t *testing.T) {
	v := classifyExit("build", exitInfo{signaled: true, signal: syscall.SIGTERM}, true, nil)
	if v.result.Outcome != sparkwing.Cancelled {
		t.Fatalf("outcome = %q, want cancelled", v.result.Outcome)
	}

	if !errors.Is(v.result.Err, context.Canceled) {
		t.Errorf("Err = %v, want it to wrap context.Canceled", v.result.Err)
	}
}

func TestClassifyExit_CancelledSIGKILLIsStillCancelled(t *testing.T) {
	v := classifyExit("build", exitInfo{signaled: true, signal: syscall.SIGKILL}, true, nil)
	if v.result.Outcome != sparkwing.Cancelled {
		t.Fatalf("outcome = %q, want cancelled", v.result.Outcome)
	}
	if strings.Contains(v.message, "out-of-memory") {
		t.Errorf("message %q blames the OOM killer for our own kill", v.message)
	}
}

func TestClassifyExit_NonZeroExitCarriesTheCode(t *testing.T) {
	v := classifyExit("build", exitInfo{code: 3}, false, nil)
	if v.result.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %q, want failed", v.result.Outcome)
	}
	if v.exitCode == nil || *v.exitCode != 3 {
		t.Fatalf("exitCode = %v, want 3", v.exitCode)
	}
	if !strings.Contains(v.message, "exited 3") {
		t.Errorf("message %q does not name the exit code", v.message)
	}
}

func TestClassifyExit_CleanExitWithoutTerminalStateFails(t *testing.T) {
	v := classifyExit("build", exitInfo{code: 0}, false, nil)
	if v.result.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %q, want failed", v.result.Outcome)
	}
	if !strings.Contains(v.message, "without writing a terminal outcome") {
		t.Errorf("message %q does not say what is missing", v.message)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:            "512B",
		2048:           "2.0KiB",
		5 << 20:        "5.0MiB",
		3 << 30:        "3.0GiB",
		1 << 40:        "1.0TiB",
		1536 * 1 << 20: "1.5GiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestMaxRSSBytes_NonPositiveIsZero(t *testing.T) {
	if got := maxRSSBytes(0); got != 0 {
		t.Errorf("maxRSSBytes(0) = %d", got)
	}
	if got := maxRSSBytes(-1); got != 0 {
		t.Errorf("maxRSSBytes(-1) = %d", got)
	}
}
