package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestRunIndexRetainsStagedContentsThroughRealPreparationAndRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and describes a real SDK pipeline")
	}
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	repo := initSnapshotRepo(t)
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, dir, `package main
import (
 "context"
 "os"
 "os/exec"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type pipeline struct{sw.Base}
func (pipeline) Help()string{
 path:=os.Getenv("SPARKWING_GATE_INDEX");if path==""{return "unbound"}
 cmd:=exec.Command("git","show",":staged.txt");cmd.Env=append(os.Environ(),"GIT_INDEX_FILE="+path)
 out,err:=cmd.CombinedOutput();if err!=nil{return "git failed: "+string(out)};return string(out)
}
func (pipeline) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 sw.Job(p,"verify",func(context.Context)error{return nil});return nil
}
func main(){sw.Register("ci",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
`)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: ci\n    entrypoint: ci\n", 0o600)
	writeSnapshotFile(t, repo, "staged.txt", "submitted index", 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "fixture")
	runSnapshotGit(t, repo, "update-index", "--split-index")
	t.Chdir(repo)
	inv, err := clisurface.Parse("sparkwing", []string{"run", "ci", "--sw-index=.git/index"})
	if err != nil {
		t.Fatal(err)
	}
	flags, args := runFlagsFromInvocation(inv)
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareNativeLocalRun(t.Context(), home, dir, "ci", os.Environ(), snapshots, flags)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.declarations.Declarations().Pipelines[0].Help != "submitted index" {
		t.Fatalf("inspection read %q", prepared.declarations.Declarations().Pipelines[0].Help)
	}
	request, err := prepareLocalRunRequest(t.Context(), prepared, flags, args)
	if err != nil {
		t.Fatal(err)
	}
	request.Request.Team = "default"
	sealed, err := runspec.SealRequest(request.Request, runspec.IntakeFacts{RunID: "index"})
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotFile(t, repo, "staged.txt", "later index", 0o600)
	runSnapshotGit(t, repo, "add", "staged.txt")
	retry, err := runspec.CarryRetry(runspec.RetryInput{Request: sealed, SourceRunID: sealed.RunID})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := source.ForRequest(retry.Request)
	if err != nil {
		t.Fatal(err)
	}
	options, err := hostSourceOptions(home, os.Environ(), secrets.NewMasker(), true)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := source.Acquire(t.Context(), spec, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	}()
	shared, err := filepath.Glob(filepath.Join(repo, ".git", "sharedindex.*"))
	if err != nil || len(shared) == 0 {
		t.Fatalf("split index fixture missing: %v", err)
	}
	for _, path := range shared {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	sessions := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: options.Base, Snapshots: snapshots}}
	planner := nodehost.SDKPlanner{Start: sessions.StartPlanning, EnsureEngine: options.Engines.Ensure, Masker: options.Base.Masker, Log: func(_ context.Context, frame nodeproto.FrameLog) error { t.Log(frame.Message); return nil }}
	result, err := planner.Prepare(t.Context(), nodehost.PlanningProcessInput{Entry: entry, Entrypoint: "ci", Environment: retry.Request.Request.Env, Args: map[string]string{}, Run: nodeproto.RunContext{RunID: "retry", Pipeline: "ci"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}, nodehost.PlanningInput{})
	if err != nil || result.Description.Help != "submitted index" {
		t.Fatalf("retry changed staged input: %+v: %v", result.Description, err)
	}
	environment, err := snapshots.Load(t.Context(), sealed.Request.Env)
	if err != nil {
		t.Fatal(err)
	}
	position := slices.IndexFunc(environment, func(item string) bool { return strings.HasPrefix(item, "SPARKWING_GATE_INDEX=") })
	if position < 0 {
		t.Fatal("index binding missing from private snapshot")
	}
	_, retained, _ := strings.Cut(environment[position], "=")
	property := func(data []byte) bool {
		if err := os.WriteFile(retained, append([]byte("corrupt"), data...), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := snapshots.Load(t.Context(), sealed.Request.Env)
		return err != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestRunIndexRefusesMissingNonregularAndMalformedInputs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	repo := initSnapshotRepo(t)
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	writeSnapshotFile(t, repo, "malformed-index", "invalid Git index", 0o600)
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"missing-index", "malformed-index", ".git"} {
		inv, err := clisurface.Parse("sparkwing", []string{"run", "ci", "--sw-index", path})
		if err != nil {
			t.Fatal(err)
		}
		flags, _ := runFlagsFromInvocation(inv)
		prepared, err := prepareNativeLocalRun(t.Context(), home, dir, "ci", os.Environ(), snapshots, flags)
		if err == nil || prepared.environment.Digest != "" {
			t.Fatalf("invalid index reached source inspection: %s: %v", path, err)
		}
	}
}

func TestParseRunFlags_Index(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--sw-index", "/tmp/snap.index"}, "/tmp/snap.index"},
		{"equals-form", []string{"--sw-index=/tmp/snap.index"}, "/tmp/snap.index"},
		{"missing-value-is-a-usage-error", []string{"--sw-index"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, pass := parseRunFlagsArgs(tc.args)
			if wf.index != tc.want {
				t.Errorf("index = %q, want %q", wf.index, tc.want)
			}
			if tc.want == "" && (wf.parseErr == nil || len(pass) != 0) {
				t.Errorf("incomplete --sw-index must be a usage error; got passthrough=%v", pass)
			}
		})
	}
}
