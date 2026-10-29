package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func FuzzLocalSchedulesRequireVerifiedTransportPeer(f *testing.F) {
	f.Add("local:0", "0", "bearer")
	f.Fuzz(func(t *testing.T, subject, uid, authorization string) {
		request := httptest.NewRequest(http.MethodPost, "http://local/api/v1/schedules/local/tick", nil)
		request.Header.Set("X-Local-UID", uid)
		request.Header.Set("Authorization", authorization)
		authorized := capability.Authorized{Target: capability.Target{Team: string(store.DefaultTeam)}, Principal: capability.Principal{Class: capability.CredentialPeer, Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectLocal, ID: subject}}}}
		_, err := new(Server).localScheduleService(request, authorized)
		var canonical *apierr.Error
		if !errors.As(err, &canonical) || canonical.Code != apierr.Forbidden {
			t.Fatal("request material substituted for kernel peer identity")
		}
	})
}

type localScheduleSnapshots struct {
	values map[runspec.Hash][]string
	loads  int
}

func (s *localScheduleSnapshots) Capture(_ context.Context, environment []string) (runspec.EnvSnapshot, error) {
	values := map[string]string{}
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	seal, err := runspec.EnvironmentDigest(values, strings.Repeat("5", 64))
	if err == nil {
		s.values[seal.Digest] = slices.Clone(environment)
	}
	return seal, err
}

func (s *localScheduleSnapshots) Load(_ context.Context, seal runspec.EnvSnapshot) ([]string, error) {
	s.loads++
	return slices.Clone(s.values[seal.Digest]), nil
}

type localScheduleListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *localScheduleListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *localScheduleListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (*localScheduleListener) Addr() net.Addr { return &net.UnixAddr{Name: "api.sock", Net: "unix"} }

type localScheduleConnection struct {
	net.Conn
	peer localhostwire.LocalPeerIdentity
}

func (c localScheduleConnection) PeerIdentity() localhostwire.LocalPeerIdentity { return c.peer }

func TestLocalScheduleTransportRegistersPinsTicksAndDeletes(t *testing.T) {
	st := newStoreForAuth(t)
	tenant, err := st.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	home, checkout := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	initializeScheduleCheckout(t, checkout)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service := &crons.Service{Operator: st.AsOperator(), Schedules: tenant, Side: store.CronWhereLocal, PinRoot: filepath.Join(home, "crons"), LockPath: filepath.Join(home, "crons.lock"), Now: func() time.Time { return now }, ArmedBy: "local:0", Host: "fixture", Version: "fixture"}
	compiled := filepath.Join(t.TempDir(), "pipeline")
	if err := os.WriteFile(compiled, []byte("proof"), 0o700); err != nil {
		t.Fatal(err)
	}
	var proved []string
	snapshots := &localScheduleSnapshots{values: map[runspec.Hash][]string{}}
	server := New(st, nil).WithLocalSchedules(service, func(_ context.Context, _, _ string, environment []string) (crons.Proof, error) {
		proved = slices.Clone(environment)
		return crons.Proof{Binary: compiled}, nil
	}, snapshots)
	listener := &localScheduleListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- wingd.ServeAPI(ctx, listener, server.HTTP()) }()
	peer, _ := localControllerIdentity(t)
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		client, host := net.Pipe()
		listener.connections <- localScheduleConnection{Conn: host, peer: peer}
		return client, nil
	}}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	options := client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
	manager, err := client.NewLocalScheduleManager(options)
	if err != nil {
		t.Fatal(err)
	}
	project, err := projectconfig.Parse([]byte("pipelines:\n  - name: ci\n    entrypoint: CI\n    on:\n      schedule:\n        - cron: '0 1 * * *'\n          where: local\n"))
	if err != nil {
		t.Fatal(err)
	}
	input := controllerwire.LocalScheduleRegistration{Mode: controllerwire.ScheduleReplace, RepoPath: checkout, Project: project, Schedules: []runspec.ScheduleDeclaration{{Pipeline: "ci", Name: "default", Cron: "0 1 * * *"}}, Environment: []string{"TOKEN=private\xff"}, Prove: true}
	written, err := manager.RegisterLocalSchedules(t.Context(), input)
	if err != nil || len(written.Schedules) != 1 {
		t.Fatalf("register through verified peer transport: %+v %v", written, err)
	}
	id := written.Schedules[0].ID
	if !slices.Equal(proved, input.Environment) {
		t.Fatal("proof changed captured inputs")
	}
	if _, err := manager.SetLocalSchedulePinned(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetLocalSchedulePinned(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.TickLocalSchedules(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	controls, err := client.NewScheduleManager(options)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	oldProver := server.localScheduleProver
	server.localScheduleProver = func(ctx context.Context, root, pipeline string, environment []string) (crons.Proof, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return crons.Proof{}, ctx.Err()
		}
		return oldProver(ctx, root, pipeline, environment)
	}
	input.Environment = []string{"TOKEN=next registration"}
	pending := make(chan error, 1)
	operationCtx, stopOperation := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopOperation()
	go func() { _, err := manager.RegisterLocalSchedules(operationCtx, input); pending <- err }()
	select {
	case <-entered:
	case <-operationCtx.Done():
		t.Fatal("registration did not enter proof")
	}
	loaded := snapshots.loads
	for _, pin := range []bool{true, false} {
		if _, err := manager.SetLocalSchedulePinned(operationCtx, id, pin); err == nil {
			t.Fatal("pin mutation overlapped registration")
		}
	}
	if _, err := controls.DeleteSchedule(operationCtx, id); err == nil {
		t.Fatal("deletion overlapped registration")
	}
	if snapshots.loads != loaded {
		t.Fatal("pin loaded stale environment while registration held its lock")
	}
	unblock()
	if err := <-pending; err != nil {
		t.Fatal(err)
	}
	server.localScheduleProver = oldProver
	if _, err := manager.SetLocalSchedulePinned(operationCtx, id, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(proved, input.Environment) {
		t.Fatal("pin used the preceding registration environment")
	}
	if _, err := controls.SetSchedulePaused(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	cron := "0 2 * * *"
	if _, err := controls.OverrideSchedule(t.Context(), id, cronspec.Override{Cron: &cron}); err != nil {
		t.Fatal(err)
	}
	if _, err := controls.DeleteSchedule(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	const remote = "https://192.0.2.1/acme/local.git"
	body := controllerwire.ScheduleRepoRequest{Mode: controllerwire.ScheduleReplace, RepoURL: remote, Branch: "main", SHA: sourcetest.FixedRemote(t, remote)}
	for i := range 21 {
		body.Schedules = append(body.Schedules, runspec.ScheduleDeclaration{Pipeline: fmt.Sprintf("local-%d", i), Cron: "0 1 * * *"})
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPut, options.URL+"/api/v1/schedules/repos", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := options.HTTP.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
		t.Fatalf("verified local principal was capped: %d %s %v %v", response.StatusCode, data, readErr, closeErr)
	}
	var registered controllerwire.ScheduleRepoWritten
	if err := json.Unmarshal(data, &registered); err != nil || len(registered.Schedules) != len(body.Schedules) {
		t.Fatalf("registered %d of %d schedules: %s %v", len(registered.Schedules), len(body.Schedules), data, err)
	}
}

func initializeScheduleCheckout(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("schedule source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source"}} {
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("prepare scheduled checkout: %v: %s", err, out)
		}
	}
}

func TestLocalControllerScheduleFiresOneSealedRunAtDueInstant(t *testing.T) {
	checkLocalControllerScheduledIntake(t, false)
}

func checkLocalControllerScheduledIntake(t *testing.T, native bool) {
	clock := &testClock{}
	st := newStoreForAuth(t, clock.option())
	tenant, err := st.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp("", "schedule-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	checkout := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	config := []byte("pipelines:\n  - name: ci\n    entrypoint: CI\n    source: origin/main\n    on:\n      schedule:\n        - cron: '* * * * *'\n          where: local\n")
	if err := os.MkdirAll(filepath.Join(checkout, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".sparkwing", projectconfig.Filename), config, 0o600); err != nil {
		t.Fatal(err)
	}
	initializeScheduleCheckout(t, checkout)
	if out, err := exec.CommandContext(t.Context(), "git", "-C", checkout, "update-ref", "refs/remotes/origin/main", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("origin/main: %s %v", out, err)
	}
	project, err := projectconfig.Parse(config)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.pin(func() time.Time { return now })
	service := &crons.Service{Operator: st.AsOperator(), Schedules: tenant, Side: store.CronWhereLocal, PinRoot: filepath.Join(home, "crons"), LockPath: filepath.Join(home, "crons.lock"), Now: func() time.Time { return now }, Host: "native", Version: "test"}
	snapshots := &localScheduleSnapshots{values: map[runspec.Hash][]string{}}
	server := New(st, nil).WithLocalSchedules(service, func(context.Context, string, string, []string) (crons.Proof, error) {
		t.Fatal("follow registration unexpectedly compiled")
		return crons.Proof{}, nil
	}, snapshots)
	service.Launcher = server.ScheduleLauncher()
	socket := filepath.Join(home, "api.sock")
	var listener net.Listener
	var transport *http.Transport
	if native {
		listener, err = wingd.ListenSocket(socket)
		if err != nil {
			t.Fatal(err)
		}
		transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return wingd.DialSocket(ctx, socket, time.Second)
		}}
	} else {
		pipe := &localScheduleListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
		listener = pipe
		peer, _ := localControllerIdentity(t)
		transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			client, host := net.Pipe()
			select {
			case pipe.connections <- localScheduleConnection{Conn: host, peer: peer}:
				return client, nil
			case <-ctx.Done():
				client.Close()
				host.Close()
				return nil, ctx.Err()
			}
		}}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- wingd.ServeAPI(ctx, listener, server.HTTP()) }()
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	options := client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: transport, Timeout: 5 * time.Second}}
	manager, err := client.NewLocalScheduleManager(options)
	if err != nil {
		t.Fatal(err)
	}
	written, err := manager.RegisterLocalSchedules(t.Context(), controllerwire.LocalScheduleRegistration{Mode: controllerwire.ScheduleReplace, RepoPath: checkout, Project: project, Schedules: []runspec.ScheduleDeclaration{{Pipeline: "ci", Cron: "* * * * *"}}, Environment: []string{"BUILD_CONTEXT=captured"}, Follow: true})
	if err != nil || len(written.Schedules) != 1 {
		t.Fatalf("arm through real api.sock: %+v %v", written, err)
	}
	row, err := tenant.GetCronSchedule(t.Context(), written.Schedules[0].ID)
	if err != nil || row.NextDueAt == nil {
		t.Fatalf("armed schedule: %+v %v", row, err)
	}
	if result, err := teststore.SQL(t, st).ExecContext(t.Context(), `UPDATE cron_schedules SET consent_json=replace(consent_json,'"dispatch":""','"dispatch":"host"') WHERE id=? AND consent_json LIKE '%"dispatch":""%'`, row.ID); err != nil {
		t.Fatal(err)
	} else if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("saved consent leaves dispatch to intake: %d %v", changed, err)
	}
	due := *row.NextDueAt
	now = due.Add(-time.Nanosecond)
	if _, err := manager.TickLocalSchedules(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	runs, err := tenant.ListRuns(t.Context(), store.RunFilter{})
	if err != nil || len(runs) != 0 {
		t.Fatalf("schedule fired before due: %d %v", len(runs), err)
	}
	now = due
	for range 2 {
		if _, err := manager.TickLocalSchedules(t.Context(), false); err != nil {
			t.Fatal(err)
		}
	}
	runs, err = tenant.ListRuns(t.Context(), store.RunFilter{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("due instant created %d runs: %v", len(runs), err)
	}
	sealed, err := tenant.ReadRun(t.Context(), runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	request := sealed.Request.Request
	if sealed.Request.Hash == "" || !sealed.Request.AdmittedAt.Equal(due) || request.Origin != runspec.OriginSchedule || request.Source.Entrypoint != "CI" || request.Source.Ref != request.Git.SHA || request.Source.RequestedRef != "origin/main" || request.Source.Location.LocalCheckout != checkout || request.Env.Digest == "" || request.Actor.ID == "" || request.Git.App.Set || !strings.HasPrefix(request.Actor.ID, "local:") {
		t.Fatalf("schedule lost sealed source or authority: %+v", sealed.Request)
	}
	if request.Dispatch != runspec.DispatchController {
		t.Fatalf("local schedule sealed %s dispatch; unselected schedules run anywhere", request.Dispatch)
	}
	fires, err := tenant.ListCronFires(t.Context(), row.ID, 10)
	if err != nil || len(fires) != 1 || fires[0].RunID != sealed.ID || !fires[0].DueAt.Equal(due) {
		t.Fatalf("fire history: %+v %v", fires, err)
	}
}
