package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"

	_ "modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestHostedSelection_FallsBackWhenTheSocketIsGone(t *testing.T) {
	home := wingdTestHome(t)
	sock := startHostedAPIFixture(t, home)
	if err := os.Remove(sock); err != nil {
		t.Fatalf("remove api socket: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	sel, err := selectHostedAPI(ctx, testWingdAdmission(home, nil), false)
	if err != nil {
		t.Fatalf("selectHostedAPI: %v", err)
	}
	if sel.sock != "" {
		t.Fatalf("socket = %q, want the standalone path", sel.sock)
	}
	if sel.standalone != standaloneDaemonFault {
		t.Fatalf("reason = %q, want %q", sel.standalone, standaloneDaemonFault)
	}
	if !strings.Contains(sel.fault, "api.sock") {
		t.Fatalf("fault = %q, want it to name the socket that would not answer", sel.fault)
	}
}

func TestHostedSelection_ForcedWhileADaemonAnswers(t *testing.T) {
	home := wingdTestHome(t)
	startHostedAPIFixture(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	sel, err := selectHostedAPI(ctx, testWingdAdmission(home, nil), true)
	if err != nil {
		t.Fatalf("selectHostedAPI: %v", err)
	}
	if sel.sock != "" {
		t.Fatalf("socket = %q, want the standalone path", sel.sock)
	}
	if sel.standalone != standaloneForced {
		t.Fatalf("reason = %q, want %q while a daemon answers", sel.standalone, standaloneForced)
	}
}

func TestHostedSelection_ForcedStillSaysNoDaemonWhenThereIsNone(t *testing.T) {
	home := wingdTestHome(t)
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	sel, err := selectHostedAPI(ctx, unhostedAdmission(home, io.Discard), true)
	if err != nil {
		t.Fatalf("selectHostedAPI: %v", err)
	}
	if sel.standalone != standaloneNoDaemon {
		t.Fatalf("reason = %q, want %q on a box with no daemon", sel.standalone, standaloneNoDaemon)
	}
}

func TestHostedSelection_ChildOfAForcedParentReadsTheParentsStore(t *testing.T) {
	home := wingdTestHome(t)
	t.Setenv("SPARKWING_HOME", home)
	startHostedAPIFixture(t, home)
	ctx := context.Background()
	paths := PathsAt(home)
	st, sa, release, err := openStandaloneStore(paths, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{ID: "parent", Pipeline: "parent", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	id, err := (localState{st: st}).EnqueueTriggerWithEnv(ctx, "child", nil, "parent", "job", "", "await-pipeline", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(StandaloneStateDBEnv, sa.stateDB)
	t.Setenv(StandaloneReasonEnv, standaloneForced)

	backends, sel, closeBackends, err := localTriggerBackends(ctx, paths, pipelineAdmission("", wingwire.OriginLocal))
	if err != nil {
		t.Fatal(err)
	}
	defer closeBackends()
	if backends.APISocket != "" || sel.standalone != standaloneForced {
		t.Fatalf("socket = %q, reason = %q; want the child forced standalone like its parent", backends.APISocket, sel.standalone)
	}
	if _, err := backends.State.GetTrigger(ctx, id); err != nil {
		t.Fatalf("child could not read its trigger from the parent's store: %v", err)
	}
}

func TestHostedSelection_AForcedReasonWithoutAParentStoreIsIgnored(t *testing.T) {
	home := wingdTestHome(t)
	t.Setenv("SPARKWING_HOME", home)
	startHostedAPIFixture(t, home)
	ctx := context.Background()
	t.Setenv(StandaloneStateDBEnv, "")
	t.Setenv(StandaloneReasonEnv, standaloneForced)
	paths := PathsAt(home)
	opts := Options{DefaultStateDB: paths.StateDB(), Admission: testWingdAdmission(home, nil)}
	backends, sel, release, err := hostedBackendsForRun(ctx, paths, &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if backends.APISocket == "" || sel.standalone != "" {
		t.Fatalf("socket = %q, reason = %q; want the daemon, since no parent named a standalone store", backends.APISocket, sel.standalone)
	}
}

func TestHostedAPIReachable_RefusesADegradedDaemon(t *testing.T) {
	sock := serveStubAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"degraded","auth":"enabled","store":"error: disk is on fire"}`))
	}))
	err := hostedAPIReachable(context.Background(), sock)
	if err == nil {
		t.Fatal("a degraded daemon was accepted as this run's host")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want it to name the answer", err)
	}
	if !errors.Is(err, errHostedStoreFault) {
		t.Fatalf("err = %v, want the fault that still refuses a run", err)
	}
	if standaloneReasonFor(err) != "" {
		t.Fatalf("an unreadable store degraded to %q", standaloneReasonFor(err))
	}
}

func TestHostedAPIReachable_DegradesOnANonJSONAnswer(t *testing.T) {
	sock := serveStubAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("nope"))
	}))
	err := hostedAPIReachable(context.Background(), sock)
	if err == nil {
		t.Fatal("a 500 was accepted as this run's host")
	}
	if errors.Is(err, errHostedStoreFault) || standaloneReasonFor(err) != "" {
		t.Fatalf("err = %v, want a daemon fault the run degrades around while naming it", err)
	}
}

func TestHostedSelection_SaysNothingBeyondTheStandaloneBlock(t *testing.T) {
	home := wingdTestHome(t)
	var lines []string
	var mu sync.Mutex
	adm := unhostedAdmission(home, io.Discard)
	adm.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	opts := Options{DefaultStateDB: PathsAt(home).StateDB(), Admission: adm}
	hosted, sel, release, err := hostedBackendsForRun(ctx, PathsAt(home), &opts)
	release()
	if err != nil {
		t.Fatalf("hostedBackendsForRun: %v", err)
	}
	if hosted.APISocket != "" {
		t.Fatal("selection found a daemon where none runs")
	}
	if sel.standalone != standaloneNoDaemon {
		t.Fatalf("reason = %q, want %q", sel.standalone, standaloneNoDaemon)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 0 {
		t.Fatalf("selection printed %d line(s) beside the standalone block: %v", len(lines), lines)
	}
}

func TestAPISocketBeside_MatchesTheDaemonLayout(t *testing.T) {
	home := wingdTestHome(t)
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	want, err := wingd.APISocketPath(home)
	if err != nil {
		t.Fatalf("api socket path: %v", err)
	}
	if got := wingd.APISocketBeside(sock); got != want {
		t.Fatalf("APISocketBeside = %q, want %q", got, want)
	}
}

func serveStubAPI(t *testing.T, h http.Handler) string {
	t.Helper()
	socketRoot := "/tmp"
	if runtime.GOOS == "windows" {
		socketRoot = os.TempDir()
	}
	dir, err := os.MkdirTemp(socketRoot, "swstub")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestHostedAPIReachable_RejectsADaemonMissingACoordinationRoute(t *testing.T) {
	sock := serveStubAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok","auth":"enabled","store":"absent"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unsupported","route":"GET ` + r.URL.Path + `"}`))
		}
	}))

	err := hostedAPIReachable(context.Background(), sock)
	if !errors.Is(err, client.ErrControllerLacksRoute) {
		t.Fatalf("err = %v, want ErrControllerLacksRoute", err)
	}
}

func TestHostedAPIReachable_AcceptsADaemonWithNoStoreYet(t *testing.T) {
	var probed []string
	var mu sync.Mutex
	sock := serveStubAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/health" {
			_, _ = w.Write([]byte(`{"status":"ok","auth":"enabled","store":"absent"}`))
			return
		}
		mu.Lock()
		probed = append(probed, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))

	if err := hostedAPIReachable(context.Background(), sock); err != nil {
		t.Fatalf("err = %v, want a fresh home to be hosted", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(probed) != len(hostedCoordinationProbes) {
		t.Fatalf("probed %d route(s) on a store-less daemon, want %d: %v",
			len(probed), len(hostedCoordinationProbes), probed)
	}
}

func TestLocalTriggerBackends_HostChildRunsAndReplays(t *testing.T) {
	home := wingdTestHome(t)
	// safety: a child run resolves the daemon from the environment, as the
	// process that spawns it does.
	t.Setenv("SPARKWING_HOME", home)
	startHostedAPIFixture(t, home)
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	backends, _, release, err := localTriggerBackends(ctx, paths, pipelineAdmission("", wingwire.OriginLocal))
	if err != nil {
		t.Fatalf("localTriggerBackends: %v", err)
	}
	defer release()

	if backends.APISocket == "" {
		t.Fatal("a child run took the direct path while the daemon serves the API socket")
	}
	if _, err := os.Stat(paths.StateDB()); err == nil {
		t.Fatalf("a child run created %s, which the daemon owns", paths.StateDB())
	}
}

func startHostedAPIFixture(t *testing.T, home string) string {
	t.Helper()
	cfg := wingd.Config{
		Home: home, Version: "test", Runs: &wingd.FuncRunStore{},
		ServeAPI: func(ctx context.Context, ln net.Listener) {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			srv := &http.Server{
				ReadHeaderTimeout: time.Second,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/api/v1/health" {
						_, _ = io.WriteString(w, `{"status":"ok","auth":"enabled","store":"ready"}`)
						return
					}
					_, _ = io.WriteString(w, `{}`)
				}),
			}
			closed := make(chan struct{})
			go func() {
				defer close(closed)
				<-ctx.Done()
				_ = srv.Close()
			}()
			_ = srv.Serve(ln)
			cancel()
			<-closed
		},
	}
	startWingdCfg(t, cfg)
	sock, err := wingd.APISocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	return sock
}
