package controller

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func FuzzLocalSchedulesRequireVerifiedTransportPeer(f *testing.F) {
	f.Add("local:0", "0", "bearer")
	f.Fuzz(func(t *testing.T, subject, uid, authorization string) {
		request := httptest.NewRequest("POST", "http://local/api/v1/schedules/local/tick", nil)
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
	seal, err := runspec.EnvironmentDigest(values)
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
	peer wingd.LocalPeerIdentity
}

func (c localScheduleConnection) PeerIdentity() wingd.LocalPeerIdentity { return c.peer }

func TestLocalScheduleTransportRegistersPinsTicksAndDeletes(t *testing.T) {
	st := newStoreForAuth(t)
	tenant, err := st.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	home, checkout := t.TempDir(), t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service := &crons.Service{Store: st, Schedules: tenant, Side: store.CronWhereLocal, PinRoot: filepath.Join(home, "crons"), LockPath: filepath.Join(home, "crons.lock"), Now: func() time.Time { return now }, ArmedBy: "local:0", Host: "fixture", Version: "fixture"}
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
	go func() { done <- server.HTTP().ServeAPI(ctx, listener) }()
	uid := uint32(123)
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		client, host := net.Pipe()
		listener.connections <- localScheduleConnection{Conn: host, peer: wingd.LocalPeerIdentity{GOOS: "linux", UID: &uid}}
		return client, nil
	}}
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
	project, err := projectconfig.Parse([]byte("pipelines:\n  - name: ci\n    entrypoint: CI\n    on:\n      schedule:\n        - cron: '0 1 * * *'\n          where: local\n"))
	if err != nil {
		t.Fatal(err)
	}
	input := controllerwire.LocalScheduleRegistration{Mode: controllerwire.ScheduleReplace, RepoPath: checkout, Project: project, Schedules: []controllerwire.ScheduleDeclaration{{Pipeline: "ci", Name: "default", Cron: "0 1 * * *"}}, Environment: []string{"TOKEN=private\xff"}, Prove: true}
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
	if _, err := controls.OverrideSchedule(t.Context(), id, controllerwire.ScheduleOverride{Cron: &cron}); err != nil {
		t.Fatal(err)
	}
	if _, err := controls.DeleteSchedule(t.Context(), id); err != nil {
		t.Fatal(err)
	}
}
