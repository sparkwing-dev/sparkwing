package artifacts

import (
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/quick"
)

func TestCaptureNeverPublishesOutsideWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows symlink privilege is required")
	}
	property := func(seed [32]byte, directory, literal bool) bool {
		data, store, records := testData()
		workspace, outside := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "private"), seed[:], 0o600); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(outside, "private")
		if directory {
			target = outside
		}
		if err := os.Symlink(target, filepath.Join(workspace, "link")); err != nil {
			t.Fatal(err)
		}
		warnings := 0
		pattern := "**"
		if literal {
			pattern = "link"
		}
		record, err := Capture(t.Context(), data, workspace, []string{pattern}, func(string) { warnings++ })
		if literal && !directory {
			return err != nil && warnings == 0 && len(records.commits) == 0 && len(store.puts) == 0
		}
		if err != nil || warnings != 1 || len(records.commits) != 1 || len(records.commits[0].Members) != 0 || len(store.puts) != 1 || store.puts[0].Digest != record.Digest {
			return false
		}
		_, published := store.values[digestOf(seed[:])]
		return !published
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128, Rand: rand.New(rand.NewSource(67))}); err != nil {
		t.Fatal(err)
	}
}
