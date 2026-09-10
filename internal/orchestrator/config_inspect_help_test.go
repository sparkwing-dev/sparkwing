package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigHelpDoesNotInspectPipeline(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "--help=true", "json"} {
		t.Run(flag, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stdout")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			original := os.Stdout
			t.Cleanup(func() { os.Stdout = original; file.Close() })
			os.Stdout = file
			args := []string{flag}
			if flag == "json" {
				args = []string{"--help", "-o", "json"}
			}
			err = runPipelineConfigInspect("unregistered-help-fixture", args)
			os.Stdout = original
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if flag == "json" && !json.Valid(raw) {
				t.Fatalf("help is not JSON: %q", raw)
			}
			if !strings.Contains(string(raw), "USAGE") || !strings.Contains(string(raw), "config") {
				t.Fatalf("help=%q", raw)
			}
		})
	}
}
