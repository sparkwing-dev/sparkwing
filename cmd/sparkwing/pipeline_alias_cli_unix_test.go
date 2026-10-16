//go:build unix

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffoldAliasThroughCLI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".sparkwing")
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, dir, "package main\nimport _ \"sdkfixture/jobs\"\nimport sw \"github.com/sparkwing-dev/sparkwing/sparkwing\"\nfunc main() { sw.Main() }\n")
	writeRepoFile(t, filepath.Join(dir, "sparkwing.yaml"), "pipelines: []\n")
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	cli := buildSubmitCLI(t)
	invoke := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), cli, args...)
		command.Dir = filepath.Dir(dir)
		var stderr strings.Builder
		command.Stderr = &stderr
		out, err := command.Output()
		if err != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, err, out, &stderr)
		}
		return out
	}
	invoke("pipeline", "new", "--pipeline=sample-report", "--template=minimal")
	var described Pipeline
	if err := json.Unmarshal(invoke("pipeline", "describe", "--pipeline=sample-report", "-o=json"), &described); err != nil {
		t.Fatal(err)
	}
	if described.Name != "sample-report" || described.Entrypoint != "SampleReport" {
		t.Fatalf("described binding: %+v", described)
	}
	if help := string(invoke("run", "sample-report", "--help", "-o=plain")); !strings.Contains(help, "sparkwing run sample-report") {
		t.Fatal(help)
	}
	var help struct{ Kind, Text string }
	if err := json.Unmarshal(invoke("run", "sample-report", "--help"), &help); err != nil {
		t.Fatal(err)
	}
	if help.Kind != "help" || !strings.Contains(help.Text, "sparkwing run sample-report") {
		t.Fatalf("selected help: %+v", help)
	}
	var plan planPreviewDoc
	if err := json.Unmarshal(invoke("pipeline", "plan", "--pipeline=sample-report", "-o=json"), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Pipeline != "sample-report" || len(plan.Jobs) == 0 {
		t.Fatalf("planned binding: %+v", plan)
	}
}
