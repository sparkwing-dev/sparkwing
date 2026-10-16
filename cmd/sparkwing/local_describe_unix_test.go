//go:build unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestLocalReplacementPipelineDescribeOverPipes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	writeSDKFixture(t, dir, `package main
import (
 "context"
 "os"
 "path/filepath"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type CI struct { sw.Base }
func (*CI) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
 if _, err := os.Stat(filepath.Join(sw.WorkDir(), "AGENTS.md")); err != nil { return err }
 sw.Job(p, "check", func(context.Context) error { return nil })
 return nil
}
func main() {
 sw.Register("ci", func() sw.Pipeline[sw.NoInputs] { return &CI{} })
 sw.Main()
}
`)
	pin, err := engine.ReadPin(dir, engine.GoSDK)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "pipeline")
	build := exec.CommandContext(t.Context(), "go", "-C", dir, "build", "-mod=mod", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local probe: %v: %s", err, output)
	}
	resolved := engine.Resolved{Identity: engine.Identity{Pin: pin, Version: "v0.66.5"}}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	git := exec.CommandContext(t.Context(), "git", "-C", root, "rev-parse", "HEAD")
	sha, err := git.Output()
	if err != nil {
		t.Fatal(err)
	}
	resolved.Identity.ManifestDigest = "fixture"
	resolved.Executable = binary
	resolved.Target = releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	entry, err := source.Acquire(t.Context(), source.Spec{RepoKey: "local:" + root, LocalCheckout: root, SHA: strings.TrimSpace(string(sha))}, source.Options{
		SDK: engine.GoSDK, Target: resolved.Target, Engines: describeEngine{resolved}, ReadPin: func(string, engine.SDKKind) (engine.Pin, error) { return pin, nil },
		InstallSDK: func(_ context.Context, _ engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
			directory, err := request.PipelineDirectory()
			return engine.SDKCommand{Directory: directory, Command: []string{binary}}, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	})
	masker := secrets.NewMasker()
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seal, err := snapshots.Capture(t.Context(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	processes := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: procenv.Base{Local: true, Captured: os.Environ(), Masker: masker}}}
	planner := nodehost.SDKPlanner{
		Start: processes.StartPlanning, Masker: masker,
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return resolved, nil
		},
		Log: func(_ context.Context, frame nodeproto.FrameLog) error { t.Log(frame.Message); return nil },
	}
	prepared, err := planner.Prepare(t.Context(), nodehost.PlanningProcessInput{
		Entry:  entry,
		Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
		Run:    nodeproto.RunContext{RunID: "probe", Pipeline: "ci"}, Args: map[string]string{},
		Environment: seal,
	}, nodehost.PlanningInput{})
	if err != nil {
		t.Fatalf("local probe describe: %v (manifest %s)", err, filepath.Join(dir, "go.mod"))
	}
	if prepared.Description.Name != "ci" || prepared.Description.Args == nil {
		t.Fatalf("local probe description: %+v", prepared.Description)
	}
	raw, err := prepared.Functions.Plan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(raw, &plan); err != nil || len(plan.Jobs) != 1 || plan.Jobs[0].ID != "check" {
		t.Fatalf("local probe plan: %s: %v", raw, err)
	}
}

type describeEngine struct{ resolved engine.Resolved }

func (e describeEngine) Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error) {
	return e.resolved, nil
}

func (e describeEngine) Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
	return e.resolved, nil
}
