package nodehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
