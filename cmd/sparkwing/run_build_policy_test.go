package main

import (
	"context"
	"debug/buildinfo"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestRunBuildPolicyReachesRealPreparationAndRetainedSource(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: compiles and describes a real SDK pipeline")
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
 _ "embed"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
//go:embed payload
var payload string
type pipeline struct{ sw.Base }
func (pipeline) Help() string { return payload }
func (pipeline) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
 sw.Job(p,"check",func(context.Context) error {return nil})
 return nil
}
func main(){sw.Register("ci",func() sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
`)
	writeSnapshotFile(t, repo, ".sparkwing/payload", "build-policy-first", 0o600)
	writeSnapshotFile(t, repo, ".gitignore", ".sparkwing/payload\n", 0o600)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: ci\n    entrypoint: ci\n", 0o600)
	t.Chdir(repo)
	runtime, err := resolveLocalRunRuntime(t.Context(), localRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.close(); err != nil {
			t.Error(err)
		}
	})
	for _, test := range []struct{ flag, want string }{
		{"", "build-policy-first"}, {"--sw-hash-all-files", "build-policy-second"}, {"--sw-no-bincache", "build-policy-third"},
	} {
		t.Run(test.flag, func(t *testing.T) {
			writeSnapshotFile(t, repo, ".sparkwing/payload", test.want, 0o600)
			args := []string{"run", "ci"}
			if test.flag != "" {
				args = append(args, test.flag)
			}
			var helpErr error
			help := captureStdout(t, func() { helpErr = runSparkwing(append(append([]string{}, args...), "--help", "-o=plain")) })
			if helpErr != nil || !strings.Contains(help, test.want) {
				t.Fatalf("pipeline help did not use selected build policy: %v: %s", helpErr, help)
			}
			if test.flag != "" {
				cached, err := readDescribeCache(t.Context(), dir)
				if err != nil || len(cached) != 1 || cached[0].Help != "build-policy-first" {
					t.Fatalf("selected build policy replaced the default description: %+v: %v", cached, err)
				}
			}
			inv, err := clisurface.Parse("sparkwing", args)
			if err != nil {
				t.Fatal(err)
			}
			flags, pass := runFlagsFromInvocation(inv)
			prepared, err := runtime.prepare(t.Context(), "ci", flags)
			if err != nil {
				t.Fatal(err)
			}
			if got := prepared.described[0].Help; got != test.want {
				t.Fatalf("compiled description=%q, want %q", got, test.want)
			}
			request, err := prepareLocalRunRequest(t.Context(), prepared, flags, pass)
			if err != nil {
				t.Fatal(err)
			}
			request.Request.Team = "default"
			sealed, err := runspec.SealRequest(request.Request, runspec.IntakeFacts{RunID: "retained"})
			if err != nil {
				t.Fatal(err)
			}
			retried, err := runspec.CarryRetry(runspec.RetryInput{Request: sealed, SourceRunID: sealed.RunID})
			if err != nil {
				t.Fatal(err)
			}
			spec, err := source.ForRequest(retried.Request)
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
			processes := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: options.Base}}
			planner := nodehost.SDKPlanner{Start: processes.StartPlanning, EnsureEngine: options.Engines.Ensure, Masker: options.Base.Masker, Log: func(_ context.Context, frame nodeproto.FrameLog) error { t.Log(frame.Message); return nil }}
			description, err := planner.Prepare(t.Context(), nodehost.PlanningProcessInput{
				Entry: entry, Entrypoint: "ci", Environment: prepared.environment,
				Run:    nodeproto.RunContext{RunID: "retained", Pipeline: "ci"},
				Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
			}, nodehost.PlanningInput{})
			if err != nil {
				t.Fatal(err)
			}
			if description.Description.Help != test.want {
				t.Fatalf("retained source compiled %q, want %q", description.Description.Help, test.want)
			}
			if test.flag == "--sw-no-bincache" {
				info, err := buildinfo.ReadFile(entry.Command[0])
				if err != nil {
					t.Fatal(err)
				}
				for _, setting := range info.Settings {
					if setting.Key == "-trimpath" && setting.Value == "true" {
						t.Fatal("retained source reused a stripped binary")
					}
				}
			}
		})
	}
}
