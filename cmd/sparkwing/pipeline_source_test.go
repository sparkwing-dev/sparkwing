package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

func declaredSourceFixture(t *testing.T) *submitTestEnv {
	t.Helper()
	e := newSubmitTestEnv(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", e.repoDir, "-c", "user.email=test@example.test", "-c", "user.name=Test"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "go.mod" {
			body = []byte(strings.Replace(string(body), "module github.com/sparkwing-dev/sparkwing", "module sourcefixture", 1) +
				fmt.Sprintf("\nrequire github.com/sparkwing-dev/sparkwing v0.0.0\nreplace github.com/sparkwing-dev/sparkwing => %q\n", root))
		}
		if err := os.WriteFile(filepath.Join(e.repoDir, ".sparkwing", name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.extraEnv = []string{"SPARKWING_WINGD_BIN=" + e.bin}
	source := `package main
import("context"; "os"; "github.com/sparkwing-dev/sparkwing/sparkwing"; "github.com/sparkwing-dev/sparkwing/pkg/runner")
type Fixture struct { sparkwing.Base }
func (*Fixture) Plan(_ context.Context, p *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
 sparkwing.Job(p,"subject",func(context.Context) error {
  return os.WriteFile(os.Getenv("SPARKWING_SUBMIT_TEST_MARKER"), []byte("selected\n"+sparkwing.WorkDir()+"\n"+os.Getenv("SPARKWING_PIPELINE_REV")),0600)
 })
 return nil
}
func main() { sparkwing.Register("fixture",func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Fixture{} }); runner.Main() }
`

	if err := os.WriteFile(filepath.Join(e.repoDir, ".sparkwing", "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := "pipelines:\n  - name: fixture\n    entrypoint: Fixture\n    source: main\n"
	if err := os.WriteFile(filepath.Join(e.repoDir, ".sparkwing", "sparkwing.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-qm", "selected source")
	git("checkout", "-qb", "feature")
	if err := os.WriteFile(filepath.Join(e.repoDir, ".sparkwing", "main.go"), []byte("caller must never compile"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("commit", "-qam", "unbuildable caller")
	return e
}

func TestDeclaredPipelineSourceForeground(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and executes the CLI and selected source")
	}
	for _, uncached := range []bool{false, true} {
		t.Run(map[bool]string{false: "cached", true: "uncached"}[uncached], func(t *testing.T) {
			e := declaredSourceFixture(t)
			if uncached {
				e.extraEnv = append(e.extraEnv, "SPARKWING_NO_BINCACHE=1")
			}
			if out, err := e.run("run", "fixture"); err != nil {
				t.Fatalf("run: %v: %s", err, out)
			}
			body, err := os.ReadFile(e.marker)
			cwd, _ := filepath.EvalSymlinks(e.repoDir)
			if err != nil || !strings.HasPrefix(string(body), "selected\n"+cwd+"\n") {
				t.Fatalf("execution = %q, %v", body, err)
			}
			if len(strings.Split(string(body), "\n")[2]) != 40 {
				t.Fatalf("missing source revision: %s", body)
			}
		})
	}
}

func TestDeclaredPipelineSourceDetached(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the CLI and starts a consumer")
	}
	e := declaredSourceFixture(t)
	ack := e.submit()
	if ack.RunID == "" {
		t.Fatal("submission succeeded without a handle")
	}
	waitUntil(t, "source execution", 30*time.Second, func() bool { _, err := os.Stat(e.marker); return err == nil })
	body, err := os.ReadFile(e.marker)
	cwd, _ := filepath.EvalSymlinks(e.repoDir)
	if err != nil || !strings.HasPrefix(string(body), "selected\n"+cwd+"\n") {
		t.Fatalf("execution = %q, %v", body, err)
	}
	trig, err := e.store().GetTrigger(t.Context(), ack.RunID)
	if err != nil || trig.TriggerEnv[orchestrator.PipelineRevKey] == "" {
		t.Fatalf("source not pinned: %v, %v", trig, err)
	}
}

func TestDeclaredPipelineSourceRejectsOverride(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and executes the CLI")
	}
	e := declaredSourceFixture(t)
	for _, detached := range []bool{false, true} {
		args := []string{"run", "fixture", "--sw-pipeline-ref", "feature"}
		if detached {
			args = append(args, "--sw-detached")
		}
		if out, err := e.run(args...); err == nil || !strings.Contains(out, "disagrees") {
			t.Fatalf("conflicting source: %v: %s", err, out)
		}
	}
}

func TestSourceScheduleProvesSelectedPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles selected schedule source")
	}
	e := declaredSourceFixture(t)
	proof, err := cronsProver(false)(t.Context(), e.repoDir, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(proof.Binary); err != nil {
		t.Fatalf("selected pipeline binary: %v", err)
	}
}
