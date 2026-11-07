package nodehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestLocalMachineAdmissionUsesCapturedHomeWithoutReadingMachineConfig(t *testing.T) {
	path, home := filepath.Join(t.TempDir(), "config.yaml"), t.TempDir()
	t.Setenv("SPARKWING_CONFIG", path)
	if err := os.WriteFile(path, []byte("malformed: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	admission := MachineAdmission(home, "v0.71.0")
	client := admission.Daemon.(machineAdmitter)
	if client.options.Home != home || client.options.Version != "v0.71.0" || !client.options.NoTakeover {
		t.Fatalf("admission changed its machine or home: %+v", admission)
	}
	if err := client.options.Spawn("foreign", "foreign"); err == nil {
		t.Fatal("native host spawned another owner")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if held, err := client.Acquire(ctx, wingwire.AdmissionRequest{RunID: "run"}, nil); held != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission: %v", err)
	}
}

func TestMachineCapacityPreflightLeavesMissingDaemonToAdmission(t *testing.T) {
	if !MachineAdmission(t.TempDir(), "v1.0.0").machineHasJobCapacity(t.Context()) {
		t.Fatal("missing ledger blocked authoritative admission")
	}
}

func TestRunnerMachineAdmissionNamesTheFixWhenNoControllerRuns(t *testing.T) {
	home := t.TempDir()
	logPath, err := wingd.LogPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("stale line from an earlier daemon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, runnerErr := RunnerMachineAdmission(home, "v0.71.0").Daemon.Acquire(ctx, wingwire.AdmissionRequest{RunID: "run"}, nil)
	for _, want := range []string{"sparkwing-runner v0.71.0", "runner.local_admission", "sparkwing dashboard start", "local_admission: false"} {
		if runnerErr == nil || !strings.Contains(runnerErr.Error(), want) {
			t.Fatalf("runner admission without a controller missing %q: %v", want, runnerErr)
		}
	}
	_, localErr := MachineAdmission(home, "v0.71.0").Daemon.Acquire(ctx, wingwire.AdmissionRequest{RunID: "run"}, nil)
	for _, err := range []error{runnerErr, localErr} {
		if !errors.Is(err, wingdclient.ErrNoDaemon) || strings.Contains(err.Error(), "stale line") {
			t.Fatalf("missing controller reported as a failed spawn with an earlier daemon's log: %v", err)
		}
	}
	if strings.Contains(localErr.Error(), "local_admission") {
		t.Fatalf("a local host received runner guidance: %v", localErr)
	}
}
