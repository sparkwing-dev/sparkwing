//go:build unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

//sparkwing:critical
func TestQueuedLandSubmissionUsesMainDefinition(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles real pipeline revisions")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "bin", "queue-land.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"older-land", "deleted-gate", "legitimate-land-change"} {
		t.Run(change, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("SPARKWING_HOME", home)
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
			t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
			repo := initSnapshotRepo(t)
			runSnapshotGit(t, repo, "symbolic-ref", "HEAD", "refs/heads/main")
			dir := filepath.Join(repo, ".sparkwing")
			fixture := `package jobs
import("context";"os";sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type args struct { Queued bool ` + "`flag:\"queued\"`" + `; QueuedMain string ` + "`flag:\"queued-main\"`" + `; Brief string ` + "`flag:\"brief\"`" + ` }
type pipeline struct{sw.Base}
func(pipeline) Plan(_ context.Context,p *sw.Plan,_ args,_ sw.RunContext)error{
sw.Job(p,"JOB",func(context.Context)error{return nil})
data,err:=os.ReadFile("candidate.txt");if err!=nil{return err};sw.Job(p,string(data),func(context.Context)error{return nil});return nil
}
func init(){sw.Register("Land",func()sw.Pipeline[args]{return pipeline{}})}
`
			writeSDKFixture(t, dir, "package main\nimport (_ \"sdkfixture/jobs\"; sw \"github.com/sparkwing-dev/sparkwing/sparkwing\")\nfunc main(){sw.Main()}\n")
			writeSnapshotFile(t, repo, ".sparkwing/jobs/land.go", strings.ReplaceAll(fixture, "JOB", "branch-gate"), 0o600)
			writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: land\n    entrypoint: Land\n", 0o600)
			writeSnapshotFile(t, repo, "bin/queue-land.sh", string(script), 0o700)
			writeSnapshotFile(t, repo, "candidate.txt", "initial-tree", 0o600)
			runSnapshotGit(t, repo, "add", ".")
			runSnapshotGit(t, repo, "commit", "-qm", "initial")
			runSnapshotGit(t, repo, "branch", "candidate")
			writeSnapshotFile(t, repo, ".sparkwing/jobs/land.go", strings.ReplaceAll(fixture, "JOB", "main-required-gate"), 0o600)
			runSnapshotGit(t, repo, "add", ".sparkwing")
			runSnapshotGit(t, repo, "commit", "-qm", "add required main gate")
			main := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
			remote := filepath.Join(t.TempDir(), "origin.git")
			runSnapshotGit(t, repo, "clone", "--quiet", "--bare", repo, remote)
			runSnapshotGit(t, repo, "remote", "add", "origin", remote)
			runSnapshotGit(t, repo, "switch", "--quiet", "candidate")
			if change != "older-land" {
				runSnapshotGit(t, repo, "merge", "--quiet", "--ff-only", "main")
				proposed := strings.ReplaceAll(fixture, "JOB", "branch-proposed-gate")
				if change == "deleted-gate" {
					proposed = strings.ReplaceAll(fixture, "sw.Job(p,\"JOB\",func(context.Context)error{return nil})", "")
				}
				writeSnapshotFile(t, repo, ".sparkwing/jobs/land.go", proposed, 0o600)
			}
			writeSnapshotFile(t, repo, "candidate.txt", change, 0o600)
			runSnapshotGit(t, repo, "add", ".")
			runSnapshotGit(t, repo, "commit", "-qm", "candidate change")
			toolDir := t.TempDir()
			capture := filepath.Join(toolDir, "arguments")
			writeSnapshotFile(t, toolDir, "sparkwing", "#!/bin/sh\nprintf '%s\\0' \"$@\" > \""+capture+"\"\n", 0o700)
			command := testgit.CommandContext(t.Context(), "bash", filepath.Join(repo, "bin", "queue-land.sh"), "-C", repo, "--brief", "Review candidate gate")
			command.Env = append(command.Env, "PATH="+toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("submission: %v\n%s", err, out)
			}
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
			inv, err := clisurface.Parse("sparkwing", argv)
			if err != nil {
				t.Fatal(err)
			}
			flags, _ := runFlagsFromInvocation(inv)
			if flags.pipelineRef != main {
				t.Errorf("queued land selected branch definition: pipeline ref = %q, want main %s", flags.pipelineRef, main)
			}
			selection, err := selectLocalRunSource(t.Context(), home, dir, "land", flags, os.Environ(), secrets.NewMasker())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := selection.execution.Close(); err != nil {
					t.Error(err)
				}
			}()
			options, err := hostSourceOptions(home, os.Environ(), secrets.NewMasker(), true)
			if err != nil {
				t.Fatal(err)
			}
			options.SDK = ""
			entry, err := source.Acquire(t.Context(), source.Spec{RepoKey: string(selection.git.Repository.Key), LocalCheckout: repo, PipelineSHA: selection.source.Ref, PipelineDir: ".sparkwing"}, options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := entry.Close(); err != nil {
					t.Error(err)
				}
			}()
			snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
			if err != nil {
				t.Fatal(err)
			}
			seal, err := snapshots.Capture(t.Context(), options.Base.Captured)
			if err != nil {
				t.Fatal(err)
			}
			processes := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: options.Base, Snapshots: snapshots}}
			planner := nodehost.SDKPlanner{Start: processes.StartPlanning, EnsureEngine: options.Engines.Ensure, Masker: options.Base.Masker, Log: func(_ context.Context, _ nodewire.FrameLog) error { return nil }}
			planned, err := planner.Prepare(t.Context(), nodehost.PlanningProcessInput{Entry: entry, Entrypoint: "Land", Environment: seal, Run: nodewire.RunContext{RunID: "queued", Pipeline: "land"}, Args: map[string]string{}, Runner: nodewire.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}, nodehost.PlanningInput{})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := planned.Plan(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var doc nodewire.PlanDoc
			if err := json.Unmarshal(plan, &doc); err != nil {
				t.Fatal(err)
			}
			var jobs []string
			for _, job := range doc.Jobs {
				jobs = append(jobs, job.ID)
			}
			if !slices.Contains(jobs, "main-required-gate") || !slices.Contains(jobs, change) {
				t.Fatalf("main's gate must judge the candidate checkout, got %v", jobs)
			}
		})
	}
}

func TestQueuedLandSubmissionRefusesUntrustedInputs(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "bin", "queue-land.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{"script", "registry"} {
		t.Run(changed, func(t *testing.T) {
			repo := initSnapshotRepo(t)
			runSnapshotGit(t, repo, "symbolic-ref", "HEAD", "refs/heads/main")
			writeSnapshotFile(t, repo, "bin/queue-land.sh", string(script), 0o700)
			writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: land\n    entrypoint: Land\n", 0o600)
			runSnapshotGit(t, repo, "add", ".")
			runSnapshotGit(t, repo, "commit", "-qm", "trusted submitter")
			remote := filepath.Join(t.TempDir(), "origin.git")
			runSnapshotGit(t, repo, "clone", "--quiet", "--bare", repo, remote)
			runSnapshotGit(t, repo, "remote", "add", "origin", remote)
			runSnapshotGit(t, repo, "switch", "--quiet", "-c", "candidate")
			if changed == "script" {
				writeSnapshotFile(t, repo, "bin/queue-land.sh", string(script)+"\n# candidate modification\n", 0o700)
			} else {
				writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: land\n    entrypoint: WeatherReport\n", 0o600)
			}
			tools := t.TempDir()
			marker := filepath.Join(tools, "invoked")
			writeSnapshotFile(t, tools, "sparkwing", "#!/bin/sh\ntouch \""+marker+"\"\n", 0o700)
			command := testgit.CommandContext(t.Context(), "bash", filepath.Join(repo, "bin/queue-land.sh"), "-C", repo, "--brief", "Review gate")
			command.Env = append(command.Env, "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "differs from origin/main") {
				t.Fatalf("untrusted %s accepted: %v\n%s", changed, err, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("untrusted input reached sparkwing: %v", err)
			}
		})
	}
}
