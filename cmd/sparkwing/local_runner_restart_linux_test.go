//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestDetachedLocalRunnerSurvivesControllerRestartAndRollsBuild(t *testing.T) {
	testDetachedLocalRunnerRestart(t, false)
}

func TestDrainingLocalOwnerAwaitsChildWhileSuccessorRunsDependencies(t *testing.T) {
	testDetachedLocalRunnerRestart(t, true)
}

func testDetachedLocalRunnerRestart(t *testing.T, childOwner bool) {
	_, bootErr := os.Stat("/run/systemd/system")
	if bootErr != nil && !errors.Is(bootErr, os.ErrNotExist) {
		t.Fatal(bootErr)
	}
	systemd := bootErr == nil
	user := t.TempDir()
	home := filepath.Join(user, "state $cash%quotes")
	t.Setenv("HOME", user)
	t.Setenv("SPARKWING_HOME", home)
	config := filepath.Join(user, "config.yaml")
	// bug: other CI shards must not starve this private controller's child admission.
	if err := os.WriteFile(config, []byte("admission:\n  budget: ignore-external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", config)
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(user, "secrets.key"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	cli := buildSubmitCLI(t)
	// safety: the waiting owner's RunAndAwait outlives every outage below, so
	// both deadlines come from one budget sized for a loaded shared machine.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	var registry *wingdclient.LocalRunnerClient
	logPath := filepath.Join(user, "controllers.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	var controller *exec.Cmd
	var controllerUnit string
	var controllerPID, sequence, restartedPID int
	manager := func(args ...string) error {
		managerCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		output, err := procenv.ControlCommandContext(managerCtx, os.Environ(), "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl: %w: %s", err, output)
		}
		return nil
	}
	var waited chan error
	var instances []localhostwire.LocalRunnerInstance
	var run, marker string
	stopController := func() {
		if restartedPID != 0 {
			if process, err := os.FindProcess(restartedPID); err == nil {
				_ = process.Kill()
			}
			restartedPID = 0
		}
		if controllerUnit != "" {
			if err := manager("kill", "--signal=KILL", controllerUnit); err != nil {
				t.Logf("controller service cleanup: %v", err)
			}
			controllerUnit = ""
		}
		if controller != nil {
			_ = controller.Process.Kill()
			<-waited
			controller = nil
		}
	}
	defer func() {
		if marker != "" {
			_ = os.WriteFile(marker+".release", nil, 0o600)
		}
		for _, instance := range instances {
			alive, err := wingd.LocalRunnerProcessAlive(instance)
			if err == nil && alive {
				process, err := os.FindProcess(instance.PID)
				if err == nil {
					_ = process.Signal(syscall.SIGTERM)
				}
			}
		}
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		stopped := func() bool {
			for _, instance := range instances {
				alive, err := wingd.LocalRunnerProcessAlive(instance)
				if err != nil || alive {
					return false
				}
			}
			return true
		}
		if pollLocalRunner(cleanup, stopped) != nil {
			t.Error("test-owned runners did not finish graceful cleanup")
			for _, instance := range instances {
				alive, err := wingd.LocalRunnerProcessAlive(instance)
				if err != nil || !alive {
					continue
				}
				if systemd {
					unit := wingd.LocalRunnerServiceName(home, instance.ID)
					cgroup, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", instance.PID))
					if err == nil && strings.Contains(string(cgroup), "/"+unit+".service") {
						if err := manager("kill", "--signal=KILL", unit); err != nil {
							t.Logf("test runner service cleanup: %v", err)
						}
					}
				} else if process, err := os.FindProcess(instance.PID); err == nil {
					_ = process.Kill()
				}
			}
		}
		stopController()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("controller output:\n%s", data)
			logs, _ := filepath.Glob(filepath.Join(home, "runners", "*", "runner.log"))
			for _, path := range logs {
				data, _ := os.ReadFile(path)
				t.Logf("runner %s output:\n%s", filepath.Base(filepath.Dir(path)), data)
			}
		}
	}()
	start := func(binary string) localhostwire.LocalRunnerInstance {
		if systemd {
			sequence++
			controllerUnit = wingd.LocalRunnerServiceName(home, fmt.Sprintf("controller-%d", sequence))
			args := []string{"--user", "--unit=" + controllerUnit, "--collect", "--service-type=exec", "--property=KillMode=control-group", "--property=StandardOutput=append:" + logPath, "--property=StandardError=append:" + logPath}
			for _, name := range []string{"HOME", "SPARKWING_HOME", "SPARKWING_CONFIG", "SPARKWING_SECRETS_KEY_FILE", "XDG_CONFIG_HOME", "PATH", "ASDF_DATA_DIR", "GOPATH", "GOMODCACHE", "GOCACHE", "GOFLAGS", "GOWORK", "TMPDIR"} {
				if value, ok := os.LookupEnv(name); ok {
					args = append(args, "--setenv="+name+"="+value)
				}
			}
			args = append(args, "--", binary, "wingd", "run", "--home", home)
			if output, err := procenv.ControlCommandContext(ctx, os.Environ(), "systemd-run", args...).CombinedOutput(); err != nil {
				t.Fatalf("controller service startup: %v: %s", err, output)
			}
		} else {
			controller = procenv.ControlCommand(os.Environ(), binary, "wingd", "run", "--home", home)
			controller.Stdout, controller.Stderr = log, log
			if err := controller.Start(); err != nil {
				t.Fatal(err)
			}
			waited = make(chan error, 1)
			command, result := controller, waited
			go func() { result <- command.Wait() }()
		}
		var instance localhostwire.LocalRunnerInstance
		var lastError error
		defer func() {
			if t.Failed() {
				t.Logf("last readiness error: %v", lastError)
			}
		}()
		awaitLocalRunnerCondition(t, ctx, func() bool {
			select {
			case err := <-waited:
				controller = nil
				t.Fatalf("controller exited during startup: %v", err)
			default:
			}
			status, err := wingdclient.QueryLocalController(ctx, home)
			if err != nil || !status.Running {
				lastError = fmt.Errorf("status %+v: %w", status, err)
				return false
			}
			controllerPID = status.PID
			if registry == nil {
				registry, err = wingdclient.NewLocalRunnerClient(home, installedVersion())
				if err != nil {
					lastError = err
					return false
				}
			}
			instance, err = registry.Active(ctx)
			lastError = err
			return err == nil && instance.State == "active"
		})
		return instance
	}
	first := start(cli)
	instances = append(instances, first)
	if first.PID == controllerPID {
		t.Fatal("runner still shares the controller process")
	}
	var connection client.EngineOptions
	if childOwner {
		run, marker, connection = startAwaitingLocalOwner(t, ctx, home, cli)
	} else {
		run, marker, connection = startHeldRunnerSchedule(t, ctx, home)
	}
	var jobPID int
	awaitLocalRunnerCondition(t, ctx, func() bool {
		data, err := os.ReadFile(marker + ".pid")
		if err != nil {
			return false
		}
		jobPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && jobPID > 0
	})
	jobBirth, err := procgroup.ProcessBirth(jobPID)
	if err != nil {
		t.Fatal(err)
	}
	heldProcess := localhostwire.LocalRunnerInstance{PID: jobPID, ProcessBirth: jobBirth}
	stopController()
	if alive, err := wingd.LocalRunnerProcessAlive(heldProcess); err != nil || !alive {
		t.Fatalf("controller service exit killed held job: alive=%t err=%v", alive, err)
	}
	if alive, err := wingd.LocalRunnerProcessAlive(first); err != nil || !alive {
		t.Fatalf("controller exit killed runner: alive=%t err=%v", alive, err)
	}
	if systemd {
		runnerCgroup, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", first.PID))
		if err != nil || !strings.Contains(string(runnerCgroup), wingd.LocalRunnerServiceName(home, first.ID)) {
			t.Fatalf("runner has no independent service cgroup: %s %v", runnerCgroup, err)
		}
	}
	reused := start(cli)
	if reused != first {
		t.Fatalf("same-build successor did not reuse identity: first=%+v successor=%+v", first, reused)
	}
	stopController()
	if err := awaitLocalRunnerClaimDrain(ctx, home, first.ID); err != nil {
		t.Fatal(err)
	}
	recovered := start(cli)
	awaitLocalRunnerCondition(t, ctx, func() bool {
		var err error
		recovered, err = registry.Active(ctx)
		return err == nil && recovered.ID != first.ID
	})
	instances = append(instances, recovered)
	old, err := registry.Read(ctx, first.ID)
	if err != nil || old.State != "draining" {
		t.Fatalf("claim exit did not fence the held runner: %+v %v", old, err)
	}
	next := filepath.Join(user, "sparkwing-next")
	source, err := os.Open(cli)
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(next, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		source.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, source)
	_, markerErr := output.WriteString("next-local-runner-build")
	if err := errors.Join(copyErr, markerErr, output.Close(), source.Close()); err != nil {
		t.Fatal(err)
	}
	restartOnto := func(binary string) {
		var stdout, stderr bytes.Buffer
		command := procenv.ControlCommandContext(ctx, os.Environ(), binary, "daemon", "restart", "--force", "-o", "json")
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("daemon restart: %v: %s", err, stderr.String())
		}
		var result struct {
			Restarted       bool   `json:"restarted"`
			State           string `json:"state"`
			PID             int    `json:"pid"`
			PreviousVersion string `json:"previous_version"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("daemon restart output %q: %v", stdout.String(), err)
		}
		if result.PID > 0 {
			restartedPID = result.PID
		}
		if !result.Restarted || result.State != "running" || result.PID <= 0 || result.PID == controllerPID || result.PreviousVersion == "" {
			t.Fatalf("daemon restart did not replace the controller: %+v (previous pid %d)", result, controllerPID)
		}
		if controller != nil {
			if err := <-waited; err != nil {
				t.Fatalf("drained controller exited with %v", err)
			}
			controller = nil
		}
		if controllerUnit != "" {
			if manager("is-active", "--quiet", controllerUnit) == nil {
				t.Fatalf("drained controller unit %s is still active", controllerUnit)
			}
			controllerUnit = ""
		}
		controllerPID = result.PID
	}
	// safety: the build roll goes through the graceful drain users run, so held
	// jobs and waiting owners must survive it as well as the kills above.
	restartOnto(next)
	rolled := first
	awaitLocalRunnerCondition(t, ctx, func() bool {
		var err error
		rolled, err = registry.Active(ctx)
		return err == nil && rolled.ID != first.ID && rolled.ID != recovered.ID
	})
	instances = append(instances, rolled)
	if rolled.Machine != first.Machine || rolled.BuildIdentity == first.BuildIdentity || rolled.PID == first.PID {
		t.Fatalf("roll did not retain machine and replace process/build: first=%+v successor=%+v", first, rolled)
	}
	if alive, err := wingd.LocalRunnerProcessAlive(first); err != nil || !alive {
		t.Fatalf("roll stopped the runner before its held job: alive=%t err=%v", alive, err)
	}
	if alive, err := wingd.LocalRunnerProcessAlive(heldProcess); err != nil || !alive {
		t.Fatalf("roll killed held job: alive=%t err=%v", alive, err)
	}
	if err := os.WriteFile(marker+".release", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	controllerClient, err := client.NewEngineClient(connection)
	if err != nil {
		t.Fatal(err)
	}
	awaitLocalRunnerCondition(t, ctx, func() bool {
		status, err := controllerClient.ReadRun(ctx, run)
		if err == nil && (status.Status == "failed" || status.Status == "cancelled") {
			t.Fatalf("held run did not survive replacement: status=%s reason=%s jobs=%+v", status.Status, status.Reason, status.Jobs)
		}
		return err == nil && status.Status == "success"
	})
	if childOwner {
		data, err := os.ReadFile(marker + ".await")
		if err != nil || string(data) != "from-child" {
			t.Fatalf("old owner did not receive child output: %s %v", data, err)
		}
	}
	if systemd {
		data, err := os.ReadFile(marker + ".after")
		if err != nil || !strings.Contains(string(data), wingd.LocalRunnerServiceName(home, rolled.ID)) {
			t.Fatalf("successor did not run the dependency: %s %v", data, err)
		}
	}
	reader, err := client.NewLogReader(connection)
	if err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	cursor := ""
	awaitLocalRunnerCondition(t, ctx, func() bool {
		logs, err := reader.ReadLogs(ctx, run, controllerwire.LogQuery{Cursor: cursor, Tree: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range logs.Records {
			lines.WriteString(record.Line)
		}
		cursor = logs.NextCursor
		return logs.Complete
	})
	for _, want := range []string{"held-begin", "held-end", "successor-after"} {
		if !strings.Contains(lines.String(), want) {
			t.Fatalf("terminal log missing %s: %s", want, lines.String())
		}
	}
	awaitLocalRunnerCondition(t, ctx, func() bool {
		alive, err := wingd.LocalRunnerProcessAlive(first)
		return err == nil && !alive
	})
}

func awaitLocalRunnerCondition(t *testing.T, ctx context.Context, condition func() bool) {
	t.Helper()
	if err := pollLocalRunner(ctx, condition); err != nil {
		t.Fatalf("local runner condition: %v", err)
	}
}

func pollLocalRunner(ctx context.Context, condition func() bool) error {
	for !condition() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		// sleepcheck:external-boundary real runner processes, services and socket state need a paced poll
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil
}

func awaitLocalRunnerClaimDrain(ctx context.Context, home, id string) error {
	socket, err := wingd.APISocketPath(home)
	if err != nil {
		return err
	}
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	drained := make(chan struct{})
	var once sync.Once
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/claim" {
			response := []byte(`{"error":{"code":"unavailable","message":"controller offline"}}`)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", strconv.Itoa(len(response)))
			w.WriteHeader(http.StatusServiceUnavailable)
			if _, err := w.Write(response); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/local/runners/"+id+"/drain" {
				once.Do(func() { close(drained) })
			}
			return
		}
		// safety: claim calls must fail as transport errors to exercise outage recovery;
		// lifecycle calls keep retrying until the successor controller answers them.
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	})}
	served := make(chan struct{})
	var serveErr error
	go func() { defer close(served); serveErr = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-served }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-served:
		return serveErr
	case <-drained:
		return server.Shutdown(ctx)
	}
}

func startHeldRunnerSchedule(t *testing.T, ctx context.Context, home string) (string, string, client.EngineOptions) {
	t.Helper()
	repo := initSnapshotRepo(t)
	directory := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, directory, `package main
import("context";"os";sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type pipeline struct{sw.Base}
func(pipeline) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 held:=sw.Job(p,"held",func(ctx context.Context)error{
  _,err:=sw.Bash(ctx,"echo $$ > \"$HOLD_MARKER.pid\"; echo held-begin; while [ ! -f \"$HOLD_MARKER.release\" ]; do sleep 0.02; done; echo held-end").Run();return err
 }).Cores(0.25)
 sw.Job(p,"after",func(ctx context.Context)error{
  data,err:=os.ReadFile("/proc/self/cgroup");if err!=nil{return err};if _,err=sw.Bash(ctx,"echo successor-after").Run();err!=nil{return err};return os.WriteFile(os.Getenv("HOLD_MARKER")+".after",data,0600)
 }).Needs(held).Cores(0.25)
 return nil
}
func main(){sw.Register("CI",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
`)
	config := []byte("pipelines:\n  - name: ci\n    entrypoint: CI\n    source: origin/main\n    on:\n      schedule:\n        - cron: '0 0 1 1 *'\n          where: local\n")
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", string(config), 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "held schedule")
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	socket, err := wingd.APISocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	options, closeTransport, err := controllerSocketOptions(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeTransport)
	local, err := client.NewLocalScheduleManager(options)
	if err != nil {
		t.Fatal(err)
	}
	project, err := projectconfig.Parse(config)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "held")
	registered, err := local.RegisterLocalSchedules(ctx, controllerwire.LocalScheduleRegistration{Mode: controllerwire.ScheduleReplace, RepoPath: repo, Project: project, Schedules: []runspec.ScheduleDeclaration{{Pipeline: "ci", Cron: "0 0 1 1 *"}}, Environment: append(os.Environ(), "HOLD_MARKER="+marker), Follow: true})
	if err != nil || len(registered.Schedules) != 1 {
		t.Fatalf("schedule registration: %+v %v", registered, err)
	}
	manager, err := client.NewScheduleManager(options)
	if err != nil {
		t.Fatal(err)
	}
	launched, err := manager.RunSchedule(ctx, registered.Schedules[0].ID, "held-runner-restart")
	if err != nil {
		t.Fatal(err)
	}
	return launched.RunID, marker, options
}

func startAwaitingLocalOwner(t *testing.T, ctx context.Context, home, binary string) (string, string, client.EngineOptions) {
	t.Helper()
	repo := initSnapshotRepo(t)
	directory := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, directory, `package main
import("context";"os";"time";sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type out struct { Value string }
type hold struct { sw.Base; sw.Produces[out] }
func (*hold) Work(w *sw.Work)(*sw.WorkStep,error){
 return sw.Step(w,"hold",func(ctx context.Context)(out,error){
  _,err:=sw.Bash(ctx,"echo $$ > \"$HOLD_MARKER.pid\"; echo held-begin; while [ ! -f \"$HOLD_MARKER.release\" ]; do sleep 0.02; done; echo held-end").Run();return out{Value:"from-child"},err
 }),nil
}
type child struct{sw.Base}
func(child) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{sw.Job(p,"held",&hold{}).Cores(0.25);return nil}
type parent struct{sw.Base}
func(parent) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 awaited:=sw.Job(p,"await",func(ctx context.Context)error{
  budget,err:=time.ParseDuration(os.Getenv("HOLD_AWAIT_TIMEOUT"));if err!=nil{return err}
  got,err:=sw.RunAndAwait[out,sw.NoInputs](ctx,"child","held",sw.WithFreshTimeout(budget));if err!=nil{return err};return os.WriteFile(os.Getenv("HOLD_MARKER")+".await",[]byte(got.Value),0600)
 }).StartsChildren("child").Cores(0.25)
 sw.Job(p,"after",func(ctx context.Context)error{
  data,err:=os.ReadFile("/proc/self/cgroup");if err!=nil{return err};if _,err=sw.Bash(ctx,"echo successor-after").Run();err!=nil{return err};return os.WriteFile(os.Getenv("HOLD_MARKER")+".after",data,0600)
 }).Needs(awaited).Cores(0.25)
 return nil
}
func main(){sw.Register("CI",func()sw.Pipeline[sw.NoInputs]{return parent{}});sw.Register("Child",func()sw.Pipeline[sw.NoInputs]{return child{}});sw.Main()}
`)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: ci\n    entrypoint: CI\n  - name: child\n    entrypoint: Child\n", 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "parent awaits child")
	marker := filepath.Join(home, "held")
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the awaiting owner needs the test's deadline")
	}
	await := "HOLD_AWAIT_TIMEOUT=" + time.Until(deadline).Round(time.Second).String()
	command := procenv.ControlCommandContext(ctx, append(os.Environ(), "HOLD_MARKER="+marker, await), binary, "run", "ci", "--sw-detached", "--sw-no-update", "-o", "json")
	command.Dir = repo
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("submit awaiting owner: %v: %s %s", err, output, stderr.String())
	}
	var receipt struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(output, &receipt); err != nil || receipt.RunID == "" {
		t.Fatalf("awaiting owner receipt: %s %v", output, err)
	}
	socket, err := wingd.APISocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	options, closeTransport, err := controllerSocketOptions(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeTransport)
	return receipt.RunID, marker, options
}
