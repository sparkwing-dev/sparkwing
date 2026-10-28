package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

func TestJSONOutputCarriesNoUpgradeNotice(t *testing.T) {
	cli := buildSubmitCLI(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SPARKWING_HOME", filepath.Join(home, ".sparkwing"))
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	stamp := paths.PathsAt(filepath.Join(home, ".sparkwing")).VersionStampFile(installsite.PathKey(cli))
	seedStamp(t, stamp, cli, "v0.0.1")
	combined, err := exec.CommandContext(t.Context(), cli, "docs", "list", "-o", "json").CombinedOutput()
	if err != nil {
		t.Fatalf("docs list: %v\n%s", err, combined)
	}
	decoder := json.NewDecoder(bytes.NewReader(combined))
	for decoder.More() {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatalf("-o json output with a pending upgrade notice is not JSON: %v\n%s", err, combined)
		}
	}
	if got := readVersionStamp(stamp); got != "v0.0.1" {
		t.Fatalf("a JSON invocation consumed the pending notice: stamp %q", got)
	}
	var stderr strings.Builder
	command := exec.CommandContext(t.Context(), cli, "docs", "list", "-o", "plain")
	command.Stderr = &stderr
	if err := command.Run(); err != nil || !strings.Contains(stderr.String(), "sparkwing changed v0.0.1 -> ") {
		t.Fatalf("the next human-facing invocation did not show the notice on stderr: %v %q", err, stderr.String())
	}
}
