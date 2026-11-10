//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestForegroundLocalRunUsesDetachedRunnerAndCapturedEnvironment(t *testing.T) {
	testLocalRunnerEnvironment(t, false)
}

func TestDetachedLocalRunSurvivesSubmitterTempRemoval(t *testing.T) {
	testLocalRunnerEnvironment(t, true)
}

func testLocalRunnerEnvironment(t *testing.T, detached bool) {
	if testing.Short() {
		t.Skip("builds a CLI and executes a pipeline through the detached runner")
	}
	user := t.TempDir()
	home := filepath.Join(user, "state")
	t.Setenv("HOME", user)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(user, "missing.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(user, "secrets.key"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	t.Setenv("FOREGROUND_CAPTURE", "from-runner-service")
	var registry *wingdclient.LocalRunnerClient
	var controller *exec.Cmd
	var waited chan error
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cleanupRegistry, registryErr := wingdclient.NewLocalRunnerClient(home, installedVersion())
		if registryErr != nil {
			t.Error(registryErr)
			return
		}
		instance, err := cleanupRegistry.Active(ctx)
		if err == nil {
			if err := cleanupRegistry.Drain(ctx, instance.ID); err != nil {
				t.Error(err)
			}
			awaitLocalRunnerCondition(t, ctx, func() bool {
				alive, err := wingd.LocalRunnerProcessAlive(instance)
				return err == nil && !alive
			})
		}
		if err := wingdclient.Stop(ctx, wingdclient.Options{Home: home}); err != nil && !errors.Is(err, wingdclient.ErrNoDaemon) {
			t.Error(err)
		}
		if controller != nil {
			select {
			case <-waited:
			case <-ctx.Done():
				_ = controller.Process.Kill()
				<-waited
			}
		}
	})
	repo := initSnapshotRepo(t)
	directory := filepath.Join(repo, ".sparkwing")
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: ci\n    entrypoint: ci\n", 0o600)
	writeSDKFixture(t, directory, `package main
import("context";"fmt";"os";sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type pipeline struct{sw.Base}
func(pipeline) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 sw.Job(p,"verify",func(context.Context)error{
  fmt.Println("foreground-runner-output")
  if gate:=os.Getenv("TEMP_GATE");gate!="" {
   if err:=os.WriteFile(gate+"-ready",[]byte("ready"),0600);err!=nil{return err}
   if _,err:=os.ReadFile(gate);err!=nil{return err}
   f,err:=os.CreateTemp("","survivor-");if err!=nil{return err};f.Close();os.Remove(f.Name())
  }
  return os.WriteFile("foreground-result",[]byte(fmt.Sprintf("%d:%s",os.Getppid(),os.Getenv("FOREGROUND_CAPTURE"))),0600)
 });return nil
}
func main(){sw.Register("ci",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
`)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "foreground fixture")
	cli := buildSubmitCLI(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	logPath := filepath.Join(user, "controller.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("controller output:\n%s", data)
		}
		_ = log.Close()
	})
	controller = procenv.ControlCommand(os.Environ(), cli, "wingd", "run", "--home", home)
	controller.Stdout, controller.Stderr = log, log
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	waited = make(chan error, 1)
	go func() { waited <- controller.Wait() }()
	awaitLocalRunnerCondition(t, ctx, func() bool {
		select {
		case err := <-waited:
			controller = nil
			t.Fatalf("controller exited during startup: %v", err)
		default:
		}
		if _, err := wingdclient.QueryLocalController(ctx, home); err != nil {
			return false
		}
		if registry == nil {
			registry, err = wingdclient.NewLocalRunnerClient(home, installedVersion())
			if err != nil {
				t.Fatal(err)
			}
		}
		_, err := registry.Active(ctx)
		return err == nil
	})
	serving, err := registry.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	preparedBy, err := os.ReadFile(cli)
	if err != nil {
		t.Fatal(err)
	}
	submitter := filepath.Join(user, "submitter")
	if err := os.WriteFile(submitter, append(preparedBy, []byte("\nforeground-submitter-build\n")...), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOREGROUND_CAPTURE", "from-submitter")
	t.Setenv("GOFLAGS", "-trimpath -p=1")
	args := []string{"run", "ci", "--sw-no-update", "-o=json"}
	var scratch, gate string
	if detached {
		scratch = t.TempDir()
		gate = filepath.Join(user, "temp-gate")
		if err := syscall.Mkfifo(gate, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(gate+"-ready", 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", scratch)
		t.Setenv("TEMP_GATE", gate)
		args = append(args, "--sw-detached")
	}
	command := procenv.ControlCommandContext(ctx, os.Environ(), submitter, args...)
	command.Dir = repo
	output, err := command.CombinedOutput()
	if detached {
		if err != nil {
			t.Fatalf("submit: %v\n%s", err, output)
		}
		var receipt struct {
			RunID string `json:"run_id"`
		}
		receiptStart := strings.LastIndex(string(output), `{"run_id"`)
		if receiptStart < 0 {
			t.Fatalf("missing receipt: %s", output)
		}
		if err := json.Unmarshal(output[receiptStart:], &receipt); err != nil || receipt.RunID == "" {
			t.Fatalf("receipt: %s (%v)", output, err)
		}
		if err := procenv.ControlCommandContext(ctx, os.Environ(), "cat", gate+"-ready").Run(); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(scratch); err != nil {
			t.Fatal(err)
		}
		if err := procenv.ControlCommandContext(ctx, os.Environ(), "sh", "-c", `printf continue > "$1"`, "sh", gate).Run(); err != nil {
			t.Fatal(err)
		}
		follow := procenv.ControlCommandContext(ctx, os.Environ(), submitter, "runs", "status", "--run", receipt.RunID, "--follow", "-o=json")
		output, err = follow.CombinedOutput()
	}
	if err != nil || !detached && !strings.Contains(string(output), "foreground-runner-output") || !detached && !strings.Contains(string(output), `"event":"run_finish"`) {
		t.Fatalf("foreground runner: %v\n%s", err, output)
	}
	instance, err := registry.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if instance != serving {
		t.Fatalf("foreground submission replaced its existing runner: before=%+v after=%+v", serving, instance)
	}
	data, err := os.ReadFile(filepath.Join(repo, "foreground-result"))
	want := fmt.Sprintf("%d:from-submitter", instance.PID)
	if err != nil || string(data) != want {
		t.Fatalf("hosting process/environment=%q, want %q: %v", data, want, err)
	}
}
