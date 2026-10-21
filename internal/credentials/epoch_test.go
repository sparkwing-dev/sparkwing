package credentials

import (
	"os"
	"testing"
)

func TestEpochSurvivesRestartAndRotates(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateEpoch(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreateEpoch(dir)
	if err != nil || again != first {
		t.Fatalf("restart: %q %v", again, err)
	}
	next, err := RotateEpoch(dir)
	if err != nil || next == first {
		t.Fatalf("rotation: %q %v", next, err)
	}
	again, err = LoadOrCreateEpoch(dir)
	if err != nil || again != next {
		t.Fatalf("rotated restart: %q %v", again, err)
	}
	if err := os.WriteFile(EpochPath(dir), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateEpoch(dir); err == nil {
		t.Fatal("accepted malformed epoch")
	}
}
