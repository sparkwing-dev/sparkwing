package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestLocalRunSelectionsReachPreparationSealingAndReplan(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles real SDK revisions")
	}
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	repo := initSnapshotRepo(t)
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := `package main
import("context";"os";"fmt";sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type pipeline struct{sw.Base}
func(pipeline) Help()string{data,err:=os.ReadFile("revision.txt");if err!=nil{return err.Error()};return "REVISION"+"/"+string(data)}
func(pipeline) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,r sw.RunContext)error{
 id:="discovery"
 if r.Pipeline!="" { data,err:=os.ReadFile("revision.txt");if err!=nil{return err};id=fmt.Sprintf("%s-%s-%s",r.Pipeline,"REVISION",data) }
 sw.Job(p,id,func(context.Context)error{return nil});return nil
}
func main(){sw.Register("OLDEntry",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Register("HEADEntry",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
`
	for _, rev := range []string{"OLD", "HEAD"} {
		writeSDKFixture(t, dir, strings.NewReplacer("REVISION", rev, "ENTRY", rev+"Entry").Replace(fixture))
		writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: "+rev+"Entry\n", 0o600)
		writeSnapshotFile(t, repo, "revision.txt", rev, 0o600)
		runSnapshotGit(t, repo, "add", ".")
		runSnapshotGit(t, repo, "commit", "-qm", rev)
		if rev == "OLD" {
			runSnapshotGit(t, repo, "tag", "OLD")
		}
	}
	old := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "OLD"))
	head := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	runSnapshotGit(t, repo, "remote", "add", "origin", "https://git.example/acme/source.git")
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", head)
	writeSnapshotFile(t, repo, "revision.txt", "DIRTY", 0o600)
	t.Chdir(repo)
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ref, code, entry, execution, sha, pipelineSHA string }{
		{"OLD", "", "OLDEntry", "OLD", old, old},
		{"", "OLD", "HEADEntry", "DIRTY", head, old},
		{"HEAD", "", "HEADEntry", "HEAD", head, head},
	} {
		t.Run(tc.ref+"/"+tc.code, func(t *testing.T) {
			args := []string{"run", "deploy-prod"}
			if tc.ref != "" {
				args = append(args, "--sw-ref", tc.ref)
			}
			if tc.code != "" {
				args = append(args, "--sw-pipeline-ref", tc.code)
			}
			inv, err := clisurface.Parse("sparkwing", args)
			if err != nil {
				t.Fatal(err)
			}
			flags, pass := runFlagsFromInvocation(inv)
			boundary := errors.New("selected engine boundary")
			wantRevision := "OLD"
			if tc.pipelineSHA == head {
				wantRevision = "HEAD"
			}
			err = dispatchRunWithEngine(inv, func(directory string, beforeExec func()) error {
				raw, readErr := os.ReadFile(filepath.Join(directory, "main.go"))
				if readErr != nil || !strings.Contains(string(raw), `return "`+wantRevision+`"`) {
					t.Fatalf("engine selected from wrong source: %v", readErr)
				}
				if beforeExec == nil {
					t.Fatal("selected source has no engine replay cleanup")
				}
				return boundary
			})
			if !errors.Is(err, boundary) {
				t.Fatalf("selected source did not reach engine selection: %v", err)
			}
			var helpErr error
			help := captureStdout(t, func() { helpErr = runSparkwing(append(append([]string{}, args...), "--help", "-o=plain")) })
			wantHelp := "OLD"
			if tc.pipelineSHA == head {
				wantHelp = "HEAD"
			}
			if helpErr != nil || !strings.Contains(help, wantHelp) {
				t.Fatalf("help selected wrong source: %v: %s", helpErr, help)
			}
			prepared, err := prepareNativeLocalRun(t.Context(), home, dir, "deploy-prod", os.Environ(), snapshots, flags)
			if err != nil {
				t.Fatal(err)
			}
			request, err := prepareLocalRunRequest(t.Context(), prepared, flags, pass)
			if err != nil {
				t.Fatal(err)
			}
			request.Request.Team = "default"
			sealed, err := runspec.SealRequest(request.Request, runspec.IntakeFacts{RunID: "selected"})
			if err != nil {
				t.Fatal(err)
			}
			if sealed.Request.Git.SHA != tc.sha || sealed.Request.Source.Ref != tc.pipelineSHA || sealed.Request.Source.Entrypoint != tc.entry {
				t.Fatalf("wrong sealed selection: git=%+v source=%+v", sealed.Request.Git, sealed.Request.Source)
			}
			if tc.code == "OLD" {
				submitted, err := prepareNativeTrigger(t.Context(), triggerFlags{pipeline: "deploy-prod", profile: "prod", ref: "HEAD", pipelineRef: "OLD"})
				if err != nil {
					t.Fatalf("native split trigger inspection: %v", err)
				}
				if submitted.Request.Git.SHA != head || submitted.Request.Source.Ref != old {
					t.Fatalf("split trigger changed revisions: %+v", submitted.Request)
				}
				projection, err := source.ForRequest(runspec.SealedRequest{Request: submitted.Request})
				if err != nil {
					t.Fatal(err)
				}
				options, err := hostSourceOptions(home, os.Environ(), secrets.NewMasker(), true)
				if err != nil {
					t.Fatal(err)
				}
				options.SDK = ""
				projection.LocalCheckout, projection.RemoteURL, projection.CommittedCheckout = repo, "", true
				hosted, err := source.Acquire(t.Context(), projection, options)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := hosted.Close(); err != nil {
						t.Error(err)
					}
				}()
				sessions := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: procenv.Base{Masker: options.Base.Masker}}}
				planner := nodehost.SDKPlanner{Start: sessions.StartPlanning, EnsureEngine: options.Engines.Ensure, Masker: options.Base.Masker, Log: func(context.Context, nodeproto.FrameLog) error { return nil }}
				selected, err := planner.PrepareCheckout(t.Context(), nodehost.CheckoutPlanningInput{Request: runspec.SealedRequest{RunID: "trigger", Request: submitted.Request}, Entry: hosted, Project: []byte("pipelines:\n  - name: deploy-prod\n    entrypoint: HEADEntry\n"), Runner: nodeproto.RunnerIdentity{Name: "runner", Type: "runner", Labels: []string{}}})
				if err != nil {
					t.Fatal(err)
				}
				plan, err := selected.Functions.Plan(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(plan), "deploy-prod-OLD-HEAD") {
					t.Fatalf("remote hosting disagrees with inspection: %s", plan)
				}
			}
			retained, err := source.ForRequest(sealed)
			if err != nil {
				t.Fatal(err)
			}
			options, err := hostSourceOptions(home, os.Environ(), secrets.NewMasker(), true)
			if err != nil {
				t.Fatal(err)
			}
			options.SDK = ""
			entry, err := source.Acquire(t.Context(), retained, options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := entry.Close(); err != nil {
					t.Error(err)
				}
			}()
			inspected, err := inspectTriggerSource(t.Context(), preparedTriggerSource{pipeline: "deploy-prod", git: sealed.Request.Git, source: sealed.Request.Source}, os.Environ(), options)
			if err != nil || len(inspected.Declarations().Pipelines) != 1 || inspected.Declarations().Pipelines[0].Help != codeRevisionForTest(tc.pipelineSHA, head)+"/"+tc.execution {
				t.Fatalf("inspection did not use hosting acquisition: %+v %v", inspected, err)
			}
			processes := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: options.Base, Snapshots: snapshots}}
			planner := nodehost.SDKPlanner{Start: processes.StartPlanning, EnsureEngine: options.Engines.Ensure, Masker: options.Base.Masker, Log: func(context.Context, nodeproto.FrameLog) error { return nil }}
			codeRevision := "OLD"
			if tc.pipelineSHA == head {
				codeRevision = "HEAD"
			}
			planned, err := planner.Prepare(t.Context(), nodehost.PlanningProcessInput{Entry: entry, Entrypoint: sealed.Request.Source.Entrypoint, Environment: sealed.Request.Env, Run: nodeproto.RunContext{RunID: "selected", Pipeline: "deploy-prod"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}, nodehost.PlanningInput{})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := planned.Functions.Plan(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var plan nodeproto.PlanDoc
			if err := json.Unmarshal(raw, &plan); err != nil {
				t.Fatal(err)
			}
			want := "deploy-prod-" + codeRevision + "-" + tc.execution
			if len(plan.Jobs) != 1 || plan.Jobs[0].ID != want {
				t.Fatalf("wrong code/execution revision: %s, want %s", raw, want)
			}
		})
	}
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: HEADEntry\n    source: OLD\n", 0o600)
	var inspectionErr error
	described := captureStdout(t, func() {
		inspectionErr = runSparkwing([]string{"pipeline", "describe", "--pipeline=deploy-prod", "-o=json"})
	})
	var schema nodeproto.PipelineDescription
	if inspectionErr != nil || json.Unmarshal([]byte(described), &schema) != nil || schema.Help != "OLD/DIRTY" {
		t.Fatalf("public describe selected wrong code: %s %v", described, inspectionErr)
	}
	preview := captureStdout(t, func() {
		inspectionErr = runSparkwing([]string{"pipeline", "plan", "--pipeline=deploy-prod", "-o=json"})
	})
	var plan planPreviewDoc
	if inspectionErr != nil || json.Unmarshal([]byte(preview), &plan) != nil || len(plan.Jobs) != 1 || plan.Jobs[0].ID != "deploy-prod-OLD-DIRTY" {
		t.Fatalf("public preview selected wrong code: %s %v", preview, inspectionErr)
	}
}

func codeRevisionForTest(sha, head string) string {
	if sha == head {
		return "HEAD"
	}
	return "OLD"
}
