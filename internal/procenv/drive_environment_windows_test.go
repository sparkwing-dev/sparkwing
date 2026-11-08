//go:build windows

package procenv

import (
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestWindowsDriveEnvironmentSurvivesLocalCapture(t *testing.T) {
	captured := []string{`=C:=C:\work`, `=D:=D:\other`, "CUSTOM=value"}
	for _, kind := range []Kind{Plan, Attempt, Install, HostChild} {
		env, err := Build(kind, Base{Local: true, Captured: captured}, Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		entries := slices.Collect(env.Values())
		for _, entry := range captured {
			if !slices.Contains(entries, entry) {
				t.Errorf("kind %d lost %q", kind, entry)
			}
		}
	}
	submitted, err := Submit(captured, []string{"CUSTOM"}, secrets.NewMasker())
	if err != nil || len(submitted) != 1 || string(submitted["CUSTOM"]) != "value" {
		t.Fatalf("submitted=%v err=%v", submitted, err)
	}
	for _, entry := range []string{"=broken=value", "=C:x=value", "=C:=bad\x00value"} {
		if _, err := Build(CLI, Base{Local: true, Captured: []string{entry}}, Runtime{}); err == nil {
			t.Fatalf("accepted %q", entry)
		}
	}
}
