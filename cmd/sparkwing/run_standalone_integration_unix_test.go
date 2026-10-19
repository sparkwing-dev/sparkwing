//go:build unix && rewrite_integration

package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestStandaloneRunOwnsControllerAndJoinsShutdown(t *testing.T) {
	probe, err := net.Listen("unix", filepath.Join(t.TempDir(), "probe.sock"))
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("sandbox refuses local sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	home, repo := t.TempDir(), initSnapshotRepo(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	directory := filepath.Join(repo, ".sparkwing")
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: ci\n    entrypoint: ci\n", 0o600)
	writeSDKFixture(t, directory, `package main
import (
 "context"
 "os"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type pipeline struct{sw.Base}
func (pipeline) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 sw.Job(p,"verify",func(context.Context)error{return os.WriteFile("standalone-result",[]byte("executed"),0600)});return nil
}
func main(){sw.Register("ci",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
`)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "standalone fixture")
	t.Chdir(repo)
	runtime, err := resolveLocalRunRuntime(t.Context(), localRunOptions{standalone: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.close(); err != nil {
			t.Error(err)
		}
	})
	if err := runtime.ensureController(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err := wingdclient.QueryLocalController(t.Context(), home)
	if err != nil || status.PID != os.Getpid() || !status.Running {
		t.Fatalf("standalone owner: %+v %v", status, err)
	}
	second, err := resolveLocalRunRuntime(t.Context(), localRunOptions{standalone: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.ensureController(t.Context()); err == nil {
		t.Fatal("second standalone opened an owned home")
	}
	if err := second.close(); err != nil {
		t.Fatal(err)
	}
	if status, err := wingdclient.QueryLocalController(t.Context(), home); err != nil || status.PID != os.Getpid() {
		t.Fatal("refused contender stopped the owner", err)
	}
	if err := runtime.close(); err != nil {
		t.Fatal(err)
	}
	inv, err := clisurface.Parse("sparkwing", []string{"run", "ci", "--sw-allow-unadmitted", "--sw-no-update", "-o=json"})
	if err != nil {
		t.Fatal(err)
	}
	flags, args := runFlagsFromInvocation(inv)
	var output bytes.Buffer
	if err := runLocalCommand(t.Context(), "ci", flags, args, &output, localRunDependencies{resolve: resolveLocalRunRuntime, version: installedVersion()}); err != nil {
		t.Fatalf("standalone run: %v\n%s", err, &output)
	}
	if !strings.Contains(output.String(), "run_finish") {
		t.Fatal("standalone did not finish through the real host", &output)
	}
	if value, err := os.ReadFile(filepath.Join(repo, "standalone-result")); err != nil || string(value) != "executed" {
		t.Fatal("pipeline did not execute", err)
	}
	if _, err := wingdclient.QueryLocalController(t.Context(), home); !errors.Is(err, wingdclient.ErrNoDaemon) {
		t.Fatal("standalone left its controller running", err)
	}
}

func TestStandaloneParentAwaitsItsChildRun(t *testing.T) {
	probe, err := net.Listen("unix", filepath.Join(t.TempDir(), "probe.sock"))
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("sandbox refuses local sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	home, repo := t.TempDir(), initSnapshotRepo(t)
	t.Setenv("SPARKWING_HOME", home)
	// One core of machine capacity: the child can start only if its waiting parent hands the slot back.
	config := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(config, []byte("admission:\n  budget: 1,ignore-external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", config)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: ci\n    entrypoint: ci\n  - name: child\n    entrypoint: child\n", 0o600)
	writeSDKFixture(t, filepath.Join(repo, ".sparkwing"), `package main
import (
 "context"
 "os"
 "time"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type out struct{Value string `+"`json:\"value\"`"+`}
type stamp struct{sw.Base; sw.Produces[out]}
func (j *stamp) Work(w *sw.Work)(*sw.WorkStep,error){return sw.Step(w,"run",func(context.Context)(out,error){return out{Value:"from-child"},nil}),nil}
type child struct{sw.Base}
func (child) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{sw.Job(p,"stamp",&stamp{});return nil}
type parent struct{sw.Base}
func (parent) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 sw.Job(p,"await",func(ctx context.Context)error{
  got,err:=sw.RunAndAwait[out,sw.NoInputs](ctx,"child","stamp",sw.WithFreshTimeout(90*time.Second))
  if err!=nil{return err}
  return os.WriteFile("child-result",[]byte(got.Value),0600)
 });return nil
}
func main(){
 sw.Register("ci",func()sw.Pipeline[sw.NoInputs]{return parent{}})
 sw.Register("child",func()sw.Pipeline[sw.NoInputs]{return child{}})
 sw.Main()
}
`)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "standalone parent and child")
	t.Chdir(repo)
	inv, err := clisurface.Parse("sparkwing", []string{"run", "ci", "--sw-allow-unadmitted", "--sw-no-update", "-o=json"})
	if err != nil {
		t.Fatal(err)
	}
	flags, args := runFlagsFromInvocation(inv)
	var output bytes.Buffer
	if err := runLocalCommand(t.Context(), "ci", flags, args, &output, localRunDependencies{resolve: resolveLocalRunRuntime, version: installedVersion()}); err != nil {
		t.Fatalf("standalone parent: %v\n%s", err, &output)
	}
	if value, err := os.ReadFile(filepath.Join(repo, "child-result")); err != nil || string(value) != "from-child" {
		t.Fatalf("standalone parent did not receive its child's output: %q %v\n%s", value, err, &output)
	}
}
