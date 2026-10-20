package nodehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestLocalMachineAdmissionUsesCapturedHomeAndSharedBoxSetting(t *testing.T) {
	path, home := filepath.Join(t.TempDir(), "config.yaml"), t.TempDir()
	t.Setenv("SPARKWING_CONFIG", path)
	if err := os.WriteFile(path, []byte("machine:\n  box_id: shared-box\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	admission, err := MachineAdmission(home, "v0.71.0")
	if err != nil {
		t.Fatal(err)
	}
	client := admission.Daemon.(machineAdmitter)
	if admission.BoxID != "shared-box" || client.options.Home != home || client.options.Version != "v0.71.0" || !client.options.NoTakeover {
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

func TestLocalMachineAdmissionRefusesBadConfigAndUsesHostnameWhenUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	admission, err := MachineAdmission(t.TempDir(), "v0.71.0")
	if err != nil || admission.BoxID != hostname {
		t.Fatalf("hostname default: %+v %v", admission, err)
	}
	for _, body := range []string{"machine:\n  box_id: ''\n", "machine:\n  unexpected: true\n", "machine: ["} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := MachineAdmission(t.TempDir(), "v0.71.0"); err == nil {
			t.Fatal("invalid machine config acquired defaults")
		}
	}
}
