//go:build !windows

package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalLoggersCreatePrivateFiles(t *testing.T) {
	for name, open := range map[string]func(string) (interface{ Close() error }, error){
		"node.log": func(path string) (interface{ Close() error }, error) {
			return newNodeLogger(path, "build", nil)
		},
		"_envelope.ndjson": func(path string) (interface{ Close() error }, error) {
			return newEnvelopeLogger(path, nil)
		},
	} {
		path := filepath.Join(t.TempDir(), name)
		logger, err := open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", name, got)
		}
	}
}
