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

func TestRegisteredNameThroughCLI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, dir, `package main
import (
 "context"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type Example struct { sw.Base }
func (*Example) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
 sw.Job(p, rc.Pipeline+"-job", func(context.Context) error { return nil })
 return nil
}
func main() {
 sw.Register("Example", func() sw.Pipeline[sw.NoInputs] { return &Example{} })
 sw.Main()
}
`)
	writeRepoFile(t, filepath.Join(dir, "sparkwing.yaml"), "pipelines:\n  - name: example\n    entrypoint: Example\n")
	tidy := exec.CommandContext(t.Context(), "go", "-C", dir, "mod", "tidy")
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("tidy SDK fixture: %v\n%s", err, output)
	}
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
	var described Pipeline
	if err := json.Unmarshal(invoke("pipeline", "describe", "--pipeline=example", "-o=json"), &described); err != nil {
		t.Fatal(err)
	}
	if described.Name != "example" || described.Entrypoint != "Example" {
		t.Fatalf("described binding: %+v", described)
	}
	if help := string(invoke("run", "example", "--help", "-o=plain")); !strings.Contains(help, "sparkwing run example") {
		t.Fatal(help)
	}
	var plan planPreviewDoc
	if err := json.Unmarshal(invoke("pipeline", "plan", "--pipeline=example", "-o=json"), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Pipeline != "example" || len(plan.Jobs) != 1 || plan.Jobs[0].ID != "example-job" {
		t.Fatalf("planned binding: %+v", plan)
	}
}
