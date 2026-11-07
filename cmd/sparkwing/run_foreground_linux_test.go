//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestForegroundLocalRunUsesDetachedRunnerAndCapturedEnvironment(t *testing.T) {
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
	command := procenv.ControlCommandContext(ctx, os.Environ(), submitter, "run", "ci", "--sw-no-update", "-o=json")
	command.Dir = repo
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "foreground-runner-output") || !strings.Contains(string(output), `"event":"run_finish"`) {
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
