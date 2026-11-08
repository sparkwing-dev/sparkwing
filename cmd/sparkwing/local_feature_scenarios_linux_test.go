//go:build linux && rewrite_integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestLocalScenarioQueuedCancellationNeverStartsJob(t *testing.T) {
	f := newLocalScenario(t)
	blocker := f.submit("blocker")
	f.awaitFile("blocker.pid")
	queued := f.submit("queued")
	awaitLocalRunnerCondition(t, f.ctx, func() bool {
		state, err := wingdclient.Query(f.ctx, wingdclient.Options{Home: f.home})
		if err != nil {
			t.Fatal(err)
		}
		for _, waiter := range state.Waiters {
			if waiter.DisplayRunID == queued+"/work" {
				return true
			}
		}
		return false
	})
	f.invoke("runs", "cancel", "--run", queued, "-o=json")
	detail := f.awaitTerminal(queued)
	if detail.Status != "cancelled" || len(detail.Jobs) != 1 || detail.Jobs[0].Outcome != "cancelled" || detail.Jobs[0].Attempt != 0 || len(detail.Attempts) != 0 {
		t.Fatalf("queued cancellation started an attempt or lost its outcome: %+v", detail)
	}
	if _, err := os.Stat(filepath.Join(f.home, "queued-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queued body executed: %v", err)
	}
	if got := f.detail(blocker); got.Status != "running" {
		t.Fatalf("cancelling the waiter disturbed the holder: %+v", got)
	}
	f.release("blocker.hold")
	if got := f.awaitTerminal(blocker); got.Status != "success" {
		t.Fatalf("holder did not complete after waiter cancellation: %+v", got)
	}
	probe := f.submit("queued")
	if got := f.awaitTerminal(probe); got.Status != "success" {
		t.Fatalf("cancelled waiter left admission blocked: %+v", got)
	}
}

func TestLocalScenarioCancellationJoinsCommandProcessGroups(t *testing.T) {
	f := newLocalScenario(t)
	run := f.submit("tree")
	var children []procgroup.Info
	for _, name := range []string{"tree.pid", "normal.pid", "escaped.pid"} {
		pid, err := strconv.Atoi(strings.TrimSpace(f.awaitFile(name)))
		if err != nil {
			t.Fatal(err)
		}
		processes, err := procgroup.ListSessions()
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range processes {
			if process.PID == pid && !process.Terminated() {
				children = append(children, process)
			}
		}
	}
	if len(children) != 3 || children[0].Group == children[2].Group || children[0].Session != children[2].Session {
		t.Fatalf("fixture did not start a child in another process group: %+v", children)
	}
	started := time.Now()
	f.invoke("runs", "cancel", "--run", run, "-o=json")
	detail := f.awaitTerminal(run)
	if detail.Status != "cancelled" || len(detail.Jobs) != 1 || detail.Jobs[0].Outcome != "cancelled" {
		t.Fatalf("command cancellation lost its durable outcome: %+v", detail)
	}
	processes, err := procgroup.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		for _, child := range children {
			if process.PID == child.PID && !process.Terminated() {
				t.Errorf("terminal run retained a runnable command child: %+v", process)
			}
		}
	}
	t.Logf("second CLI cancel to terminal history and joined process groups: %s", time.Since(started))
	logs := f.invoke("runs", "logs", "--run", run, "--follow")
	if !bytes.Contains(logs, []byte("tree-ready")) {
		t.Fatalf("cancelled run lost its command logs: %s", logs)
	}
}

func TestLocalScenarioDetachedChildSurvivesParent(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_parent=%t", cancelParent), func(t *testing.T) {
			f := newLocalScenario(t)
			t.Setenv("SCENARIO_HOLD_PARENT", strconv.FormatBool(cancelParent))
			parent := f.submit("parent")
			child := f.awaitFile("child.id")
			f.awaitFile("child.pid")
			want := "success"
			if cancelParent {
				f.awaitFile("parent.pid")
				f.invoke("runs", "cancel", "--run", parent, "-o=json")
				want = "cancelled"
			}
			if got := f.awaitTerminal(parent); got.Status != want {
				t.Fatalf("parent outcome=%s, want %s", got.Status, want)
			}
			if got := f.detail(child); got.Status != "running" || got.CancelRequestedAt != nil {
				t.Fatalf("detached child did not outlive its terminal parent: %+v", got)
			}
			f.release("child.hold")
			if got := f.awaitTerminal(child); got.Status != "success" {
				t.Fatalf("detached child could not finish independently: %+v", got)
			}
			logs := f.invoke("runs", "logs", "--run", child, "--follow")
			if !bytes.Contains(logs, []byte("child-finished")) {
				t.Fatalf("detached child lost independent history: %s", logs)
			}
		})
	}
}

type localScenario struct {
	t               *testing.T
	ctx             context.Context
	cli, home, repo string
}

func newLocalScenario(t *testing.T) *localScenario {
	t.Helper()
	cli := buildSubmitCLI(t)
	_ = sdkFixtureModuleRoot(t)
	for _, name := range []string{"GOCACHE", "GOMODCACHE"} {
		t.Setenv(name, offlineGoEnv(t, name))
	}
	user := t.TempDir()
	home := filepath.Join(user, "state")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", user)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(user, "cache"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(user, "secrets.key"))
	t.Setenv("SCENARIO_DIR", home)
	config := filepath.Join(user, "config.yaml")
	t.Setenv("SPARKWING_CONFIG", config)
	if err := os.WriteFile(config, []byte("admission:\n  budget: 1,ignore-external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"blocker.hold", "normal.hold", "escaped.hold", "child.hold", "parent.hold"} {
		if err := unix.Mkfifo(filepath.Join(home, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)
	f := &localScenario{t: t, ctx: ctx, cli: cli, home: home, repo: initSnapshotRepo(t)}
	var configBody strings.Builder
	configBody.WriteString("pipelines:\n")
	for _, name := range []string{"blocker", "queued", "tree", "parent", "child"} {
		fmt.Fprintf(&configBody, "  - name: %s\n    entrypoint: %s\n", name, name)
	}
	writeSnapshotFile(t, f.repo, ".sparkwing/sparkwing.yaml", configBody.String(), 0o600)
	writeSDKFixture(t, filepath.Join(f.repo, ".sparkwing"), localScenarioPipeline)
	runSnapshotGit(t, f.repo, "add", ".")
	runSnapshotGit(t, f.repo, "commit", "-qm", "local feature scenarios")
	logPath := filepath.Join(user, "controller.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	controller := procenv.ControlCommand(os.Environ(), cli, "wingd", "run", "--home", home)
	controller.Stdout, controller.Stderr = log, log
	if err := controller.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- controller.Wait() }()
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		for _, name := range []string{"blocker.hold", "normal.hold", "escaped.hold", "child.hold", "parent.hold"} {
			pipe, err := os.OpenFile(filepath.Join(home, name), os.O_WRONLY|unix.O_NONBLOCK, 0)
			if err == nil {
				_, err = pipe.WriteString("release\n")
				if err := errors.Join(err, pipe.Close()); err != nil {
					t.Error(err)
				}
			} else if !errors.Is(err, unix.ENXIO) {
				t.Error(err)
			}
		}
		if err := wingdclient.Stop(cleanup, wingdclient.Options{Home: home, RetireRunner: true}); err != nil && !errors.Is(err, wingdclient.ErrNoDaemon) {
			t.Error(err)
		}
		select {
		case <-done:
		case <-cleanup.Done():
			_ = controller.Process.Kill()
			<-done
			t.Error("controller did not join shutdown")
		}
		_ = log.Close()
		if t.Failed() {
			body, _ := os.ReadFile(logPath)
			t.Logf("controller: %s", body)
		}
	})
	awaitLocalRunnerCondition(t, ctx, func() bool {
		_, err := wingdclient.QueryLocalController(ctx, home)
		return err == nil
	})
	return f
}

func (f *localScenario) invoke(args ...string) []byte {
	f.t.Helper()
	command := procenv.ControlCommandContext(f.ctx, os.Environ(), f.cli, args...)
	command.Dir = f.repo
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	var exited *exec.ExitError
	status := len(args) > 1 && args[0] == "runs" && args[1] == "status"
	observed := status && errors.As(err, &exited) && (exited.ExitCode() == 1 || exited.ExitCode() == 3)
	if err != nil && !observed {
		f.t.Fatalf("CLI %v: %v\n%s\n%s", args, err, output, &stderr)
	}
	return output
}

func (f *localScenario) submit(pipeline string) string {
	f.t.Helper()
	var receipt struct {
		RunID string `json:"run_id"`
	}
	output := f.invoke("run", pipeline, "--sw-detached", "--sw-no-update", "-o=json")
	if err := json.Unmarshal(output, &receipt); err != nil || receipt.RunID == "" {
		f.t.Fatalf("run receipt: %s: %v", output, err)
	}
	return receipt.RunID
}

func (f *localScenario) detail(run string) controllerwire.RunDetail {
	f.t.Helper()
	var detail controllerwire.RunDetail
	if err := json.Unmarshal(f.invoke("runs", "status", "--run", run, "-o=json"), &detail); err != nil {
		f.t.Fatal(err)
	}
	return detail
}

func (f *localScenario) awaitTerminal(run string) controllerwire.RunDetail {
	f.t.Helper()
	var detail controllerwire.RunDetail
	awaitLocalRunnerCondition(f.t, f.ctx, func() bool {
		detail = f.detail(run)
		return detail.Status == "success" || detail.Status == "failed" || detail.Status == "cancelled"
	})
	return detail
}

func (f *localScenario) awaitFile(name string) string {
	f.t.Helper()
	var body []byte
	awaitLocalRunnerCondition(f.t, f.ctx, func() bool {
		var err error
		body, err = os.ReadFile(filepath.Join(f.home, name))
		return err == nil && len(body) != 0
	})
	return strings.TrimSpace(string(body))
}

func (f *localScenario) release(name string) {
	f.t.Helper()
	pipe, err := os.OpenFile(filepath.Join(f.home, name), os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	_, err = pipe.WriteString("release\n")
	if err := errors.Join(err, pipe.Close()); err != nil {
		f.t.Fatal(err)
	}
}

const localScenarioPipeline = `package main
import("context";"os";"path/filepath";sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type pipeline struct{sw.Base; name string}
func(p pipeline) Plan(_ context.Context,plan *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 cores:=0.25;if p.name=="blocker"{cores=1}
 sw.Job(plan,"work",func(ctx context.Context)error{
  dir:=os.Getenv("SCENARIO_DIR")
  hold:=func(name string)error{_,err:=sw.Bash(ctx,"echo $$ > \"$SCENARIO_DIR/"+name+".pid\"; echo "+name+"-ready; read line < \"$SCENARIO_DIR/"+name+".hold\"; echo "+name+"-finished").Run();return err}
  switch p.name{
  case "blocker","child":return hold(p.name)
  case "queued":return os.WriteFile(filepath.Join(dir,"queued-ran"),[]byte("ran"),0600)
  case "parent":
   child,err:=sw.StartChild(ctx,"child",nil);if err!=nil{return err}
   if err=os.WriteFile(filepath.Join(dir,"child.id"),[]byte(child),0600);err!=nil{return err}
   if os.Getenv("SCENARIO_HOLD_PARENT")=="true"{return hold("parent")};return nil
  case "tree":
   _,err:=sw.Bash(ctx,"echo $$ > \"$SCENARIO_DIR/tree.pid\"; sh -c 'echo $$ > \"$SCENARIO_DIR/normal.pid\"; read line < \"$SCENARIO_DIR/normal.hold\"' & timeout 300 sh -c 'echo $$ > \"$SCENARIO_DIR/escaped.pid\"; read line < \"$SCENARIO_DIR/escaped.hold\"' & echo tree-ready; wait").Run();return err
  };return nil
 }).Cores(cores).StartsChildren("child");return nil
}
func main(){for _,name:=range []string{"blocker","queued","tree","parent","child"}{sw.Register(name,func()sw.Pipeline[sw.NoInputs]{return pipeline{name:name}})};sw.Main()}
`
