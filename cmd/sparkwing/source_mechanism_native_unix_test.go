//go:build unix && rewrite_integration

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestNativeScheduleOriginMainRunsToCompletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(home, "secrets.key"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	marker := filepath.Join(home, "completed")
	t.Setenv("W9_SCHEDULE_MARKER", marker)
	repo := initSnapshotRepo(t)
	directory := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, directory, `package main
import("context";"os";sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type pipeline struct{sw.Base}
func(pipeline) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 sw.Job(p,"verify",func(context.Context)error{return os.WriteFile(os.Getenv("W9_SCHEDULE_MARKER"),[]byte("complete"),0600)});return nil
}
func main(){sw.Register("CI",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
`)
	config := []byte("pipelines:\n  - name: ci\n    entrypoint: CI\n    source: origin/main\n    on:\n      schedule:\n        - cron: '* * * * *'\n          where: local\n")
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", string(config), 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "scheduled source")
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	cfg, err := resolveLocalRuntimeConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan *store.Store, 1)
	cfg.Open = func(ctx context.Context, home string) (wingd.LocalController, error) {
		return (localcontroller.Config{Bind: func(ctx context.Context, binding localcontroller.Binding) (localcontroller.Services, error) {
			services, err := bindLocalController(ctx, binding)
			if err == nil {
				teststore.RegisterSQLite(binding.Store, filepath.Join(binding.Home, "state.db"))
				opened <- binding.Store
			}
			return services, err
		}}).Open(ctx, home)
	}
	runtime, err := wingd.NewLocalRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		err := runtime.Run(ctx, wingd.LocalStartOptions{Home: home, Version: installedVersion()})
		done <- err
		cancel()
	}()
	t.Cleanup(func() {
		cancel()
		if err := runtime.Drain(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	_, err = runtime.AwaitReady(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := <-opened
	socket, err := wingd.APISocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	options, closeTransport, err := controllerSocketOptions(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	manager, err := client.NewLocalScheduleManager(options)
	if err != nil {
		t.Fatal(err)
	}
	project, err := projectconfig.Parse(config)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := manager.RegisterLocalSchedules(ctx, controllerwire.LocalScheduleRegistration{Mode: controllerwire.ScheduleReplace, RepoPath: repo, Project: project, Schedules: []runspec.ScheduleDeclaration{{Pipeline: "ci", Cron: "* * * * *"}}, Environment: os.Environ(), Follow: true})
	if err != nil || len(registered.Schedules) != 1 {
		t.Fatalf("registration: %+v %v", registered, err)
	}
	due := time.Now().Truncate(time.Minute)
	if _, err := teststore.SQL(t, state).ExecContext(ctx, `UPDATE cron_schedules SET cursor_at=?,next_due_at=? WHERE id=?`, due.Add(-time.Minute).UnixNano(), due.UnixNano(), registered.Schedules[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.TickLocalSchedules(ctx, false); err != nil {
		t.Fatal(err)
	}
	tenant, err := state.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := tenant.ListRuns(ctx, store.RunFilter{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("scheduled intake: %+v %v", runs, err)
	}
	if sealed, err := tenant.ReadRun(ctx, runs[0].ID); err != nil || sealed.Request.Request.Dispatch != runspec.DispatchController {
		t.Fatalf("local schedule must run anywhere under controller dispatch: %+v %v", sealed.Request.Request.Dispatch, err)
	}
	output := captureStdout(t, func() {
		err = runSparkwing([]string{"runs", "status", "--run", runs[0].ID, "--follow", "--timeout", "45s", "-o=json"})
	})
	if err != nil {
		t.Fatalf("scheduled hosting: %v %s", err, output)
	}
	body, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(body)) != "complete" {
		t.Fatalf("scheduled step did not complete: %q %v", body, err)
	}
}
