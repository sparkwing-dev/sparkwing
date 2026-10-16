package nodehost

import (
	"os"
	"reflect"
	"testing"
)

func TestCapturedEnvironmentNativeBytesCannotReplaceAnotherSnapshot(t *testing.T) {
	snapshots, err := NewFileEnvironmentSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := []string{"VALUE=\xff"}
	second := []string{"VALUE=\xfe"}
	firstSeal, err := snapshots.Capture(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	secondSeal, err := snapshots.Capture(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := snapshots.Load(t.Context(), firstSeal)
	if err != nil || !reflect.DeepEqual(loaded, first) {
		t.Fatalf("second capture replaced the first snapshot: %q, %v", loaded, err)
	}
	data, err := os.ReadFile(snapshots.path(secondSeal))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshots.path(firstSeal), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshots.Load(t.Context(), firstSeal); err == nil {
		t.Fatal("substitution of native bytes passed sealed snapshot verification")
	}
}
