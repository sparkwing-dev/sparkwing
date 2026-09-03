package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	storagefs "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type hostedMemoPipe struct{ sparkwing.Base }

var hostedMemoBodies atomic.Int32

func (hostedMemoPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	first := sparkwing.Job(plan, "first", func(context.Context) error {
		hostedMemoBodies.Add(1)
		return nil
	}).Memoize(func(context.Context) sparkwing.CacheKey { return sparkwing.Key("hosted", "memo-v1") })
	sparkwing.Job(plan, "second", func(context.Context) error { return nil }).Needs(first)
	return nil
}

var hostedRegister sync.Once

func registerHostedPipelines(t *testing.T) {
	t.Helper()
	hostedRegister.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("hosted-memo", func() sparkwing.Pipeline[sparkwing.NoInputs] {
			return hostedMemoPipe{}
		})
	})
}

type storeOpenLog struct {
	mu    sync.Mutex
	paths []string
}

func (l *storeOpenLog) record(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, path)
}

func (l *storeOpenLog) Load() int32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int32(len(l.paths))
}

func (l *storeOpenLog) opened(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, p := range l.paths {
		if p == path {
			n++
		}
	}
	return n
}

func countStoreOpens(t *testing.T) *storeOpenLog {
	t.Helper()
	log := &storeOpenLog{}
	previousSpec, previousOpen := openStateStoreFromSpec, storeOpen
	openStateStoreFromSpec = func(ctx context.Context, spec backends.Spec, lookup storeurl.ProfileLookup) (storage.StateStore, error) {
		log.record(spec.Path)
		return previousSpec(ctx, spec, lookup)
	}
	storeOpen = func(path string) (*store.Store, error) {
		log.record(path)
		return previousOpen(path)
	}
	t.Cleanup(func() { openStateStoreFromSpec, storeOpen = previousSpec, previousOpen })
	return log
}

func TestHostedRun_OpensNoStoreAndMintsNoToken(t *testing.T) {
	registerHostedPipelines(t)
	home := wingdTestHome(t)
	_, runs := startAPIDaemon(t, home, nil)
	paths := PathsAt(home)
	opens := countStoreOpens(t)

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	res, err := RunLocal(ctx, paths, Options{
		Pipeline:  "hosted-memo",
		Admission: testWingdAdmission(home, nil),
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (%v)", res.Status, res.Error)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("the run opened the store %d times, want 0", got)
	}

	rw, _, err := runs.Handles(ctx)
	if err != nil {
		t.Fatalf("daemon store handles: %v", err)
	}
	run, err := rw.GetRun(ctx, res.RunID)
	if err != nil {
		t.Fatalf("the daemon's store has no row for %s: %v", res.RunID, err)
	}
	if run.Status != "success" {
		t.Fatalf("run row status = %q, want success", run.Status)
	}
	nodes, err := rw.ListNodes(ctx, res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	tokens, err := rw.ListTokens("", true)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("a hosted run left %d tokens behind, want 0", len(tokens))
	}
}

func TestHostedRun_MemoSlotIsArbitratedByTheDaemon(t *testing.T) {
	registerHostedPipelines(t)
	home := wingdTestHome(t)
	_, runs := startAPIDaemon(t, home, nil)
	paths := PathsAt(home)
	hostedMemoBodies.Store(0)

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	var last *Result
	for attempt := range 2 {
		res, err := RunLocal(ctx, paths, Options{
			Pipeline:  "hosted-memo",
			Admission: testWingdAdmission(home, nil),
		})
		if err != nil {
			t.Fatalf("run %d: %v", attempt, err)
		}
		if res.Status != "success" {
			t.Fatalf("run %d status = %q (%v)", attempt, res.Status, res.Error)
		}
		last = res
	}
	if got := hostedMemoBodies.Load(); got != 1 {
		t.Fatalf("the memoized body ran %d times across two runs, want 1", got)
	}

	rw, _, err := runs.Handles(ctx)
	if err != nil {
		t.Fatalf("daemon store handles: %v", err)
	}
	nodes, err := rw.ListNodes(ctx, last.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for _, n := range nodes {
		if n.NodeID == "first" && n.Outcome != string(sparkwing.Cached) {
			t.Fatalf("second run's memoized node outcome = %q, want cached", n.Outcome)
		}
	}
	states, err := rw.ListConcurrencyStates(ctx)
	if err != nil {
		t.Fatalf("ListConcurrencyStates: %v", err)
	}
	for _, st := range states {
		if len(st.Holders) != 0 {
			t.Fatalf("key %s still holds %d slots after the runs", st.Key, len(st.Holders))
		}
	}
}

func TestHostedSelection_StandaloneWhenTheDaemonServesNoAPI(t *testing.T) {
	registerHostedPipelines(t)
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	paths := PathsAt(home)
	opens := countStoreOpens(t)
	warnings := captureStandaloneWarnings(t)

	var lines []string
	var mu sync.Mutex
	adm := testWingdAdmission(home, nil)
	adm.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	res, err := RunLocal(ctx, paths, Options{Pipeline: "hosted-memo", Admission: adm})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (%v)", res.Status, res.Error)
	}
	if got := opens.opened(paths.StateDB()); got != 0 {
		t.Fatalf("the run opened this machine's shared store %d times, want 0", got)
	}
	if got := opens.opened(paths.StandaloneStateDB()); got != 1 {
		t.Fatalf("the run opened %s %d times, want 1", paths.StandaloneStateDB(), got)
	}
	if got := warnings.String(); !strings.Contains(got, "predates this pipeline's SDK") {
		t.Fatalf("stderr block = %q, want the daemon-older branch", got)
	}
}

func TestHostedSelection_FallsBackWhenTheSocketIsGone(t *testing.T) {
	home := wingdTestHome(t)
	sock, _ := startAPIDaemon(t, home, nil)
	if err := os.Remove(sock); err != nil {
		t.Fatalf("remove api socket: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	sel, err := selectHostedAPI(ctx, testWingdAdmission(home, nil))
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
	startAPIDaemon(t, home, nil)
	t.Setenv(AllowUnadmittedEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	sel, err := selectHostedAPI(ctx, testWingdAdmission(home, nil))
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
	t.Setenv(AllowUnadmittedEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	sel, err := selectHostedAPI(ctx, unhostedAdmission(home, io.Discard))
	if err != nil {
		t.Fatalf("selectHostedAPI: %v", err)
	}
	if sel.standalone != standaloneNoDaemon {
		t.Fatalf("reason = %q, want %q on a box with no daemon", sel.standalone, standaloneNoDaemon)
	}
}

func TestHostedRun_SkewedDaemonStoreRunsStandalone(t *testing.T) {
	registerHostedPipelines(t)
	home := wingdTestHome(t)
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}

	// safety: the daemon holds a store that records a requirement its binary
	// does not understand, which is the daemon being older than the pin that
	// migrated it. Two homes because one process cannot be both binaries at
	// once.
	brokenHome := wingdTestHome(t)
	requireUnknownFeature(t, PathsAt(brokenHome).StateDB())
	startAPIDaemonOnFaultedStore(t, home, brokenHome)

	opens := countStoreOpens(t)
	warnings := captureStandaloneWarnings(t)
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	res, err := RunLocal(ctx, paths, Options{Pipeline: "hosted-memo", Admission: testWingdAdmission(home, nil)})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (%v), want success", res.Status, res.Error)
	}
	if got := opens.opened(paths.StateDB()); got != 0 {
		t.Fatalf("the run opened this machine's shared store %d times, want 0", got)
	}
	if got := opens.opened(paths.StandaloneStateDB()); got != 1 {
		t.Fatalf("the run opened %s %d times, want 1", paths.StandaloneStateDB(), got)
	}
	block := warnings.String()
	if !strings.Contains(block, "predates this pipeline's SDK") {
		t.Fatalf("stderr block = %q, want the daemon-older branch", block)
	}
	if !strings.Contains(block, "(test)") {
		t.Fatalf("stderr block = %q, want it to name the daemon's version", block)
	}
}

func TestHostedRun_UnreadableDaemonStoreRefusesTheRun(t *testing.T) {
	registerHostedPipelines(t)
	home := wingdTestHome(t)
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}

	// safety: a file that is not a database is unreadable for a reason age
	// cannot explain, so it is the fault a run is still refused for.
	brokenHome := wingdTestHome(t)
	if err := PathsAt(brokenHome).EnsureRoot(); err != nil {
		t.Fatalf("ensure broken root: %v", err)
	}
	if err := os.WriteFile(PathsAt(brokenHome).StateDB(), []byte("this is not a database"), 0o600); err != nil {
		t.Fatalf("write a non-database: %v", err)
	}
	startAPIDaemonOnFaultedStore(t, home, brokenHome)

	opens := countStoreOpens(t)
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	_, err := RunLocal(ctx, paths, Options{Pipeline: "hosted-memo", Admission: testWingdAdmission(home, nil)})
	if err == nil {
		t.Fatal("a daemon reporting an unreadable runs store was accepted")
	}
	if !strings.Contains(err.Error(), "runs store") {
		t.Fatalf("err = %v, want it to name the daemon's store", err)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("a refused run opened %d store(s), want 0", got)
	}
}

// safety: admission answers from this home while the API socket answers from a
// store the daemon cannot open, which is the split a real skew produces
// between a daemon that still admits and one that cannot serve state.
func startAPIDaemonOnFaultedStore(t *testing.T, home, faultedHome string) {
	t.Helper()
	faulted, err := NewHeldRunStore(faultedHome)
	if err != nil {
		t.Fatalf("held run store: %v", err)
	}
	t.Cleanup(func() { _ = faulted.Close() })
	admissionRuns, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("held run store: %v", err)
	}
	t.Cleanup(func() { _ = admissionRuns.Close() })
	startAPIDaemonSplit(t, home, admissionRuns, faulted, nil, nil)
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

func TestHostedAPIReachable_ASkewedStoreDegradesRatherThanRefuses(t *testing.T) {
	sock := serveStubAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"degraded","auth":"enabled",` +
			`"store":"skew: sparkwing: this state database uses a-feature-from-the-future, which needs sparkwing >= v99.0.0"}`))
	}))
	err := hostedAPIReachable(context.Background(), sock)
	if err == nil {
		t.Fatal("a daemon too old for this home's store was accepted as this run's host")
	}
	if errors.Is(err, errHostedStoreFault) {
		t.Fatalf("err = %v, want age rather than a fault", err)
	}
	if got := standaloneReasonFor(err); got != standaloneDaemonOlder {
		t.Fatalf("standaloneReasonFor(%v) = %q, want %q", err, got, standaloneDaemonOlder)
	}
	if !strings.Contains(err.Error(), "a-feature-from-the-future") {
		t.Fatalf("err = %v, want it to carry the daemon's own message", err)
	}
}

func TestStoreHealthState_SplitsSkewFromFault(t *testing.T) {
	skew := &store.SkewError{DBVersion: 99, BinaryVersion: 27, Requirements: []string{"a-feature-from-the-future"}}
	if got := storeHealthState(fmt.Errorf("open: %w", skew)); !strings.HasPrefix(got, "skew: ") {
		t.Errorf("storeHealthState(skew) = %q, want a skew: prefix", got)
	}
	if got := storeHealthState(errors.New("disk is on fire")); !strings.HasPrefix(got, "error: ") {
		t.Errorf("storeHealthState(fault) = %q, want an error: prefix", got)
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
	dir, err := os.MkdirTemp("/tmp", "swstub")
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
	startAPIDaemon(t, home, nil)
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	backends, _, release, err := localTriggerBackends(ctx, paths, "")
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

func TestLocalTriggerBackends_ANamedProfileKeepsItsOwnStore(t *testing.T) {
	home := wingdTestHome(t)
	// safety: a child run resolves the daemon from the environment, as the
	// process that spawns it does.
	t.Setenv("SPARKWING_HOME", home)
	startAPIDaemon(t, home, nil)
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	if _, _, release, err := localTriggerBackends(ctx, paths, "does-not-exist"); err == nil {
		release()
		t.Fatal("a named profile resolved to the daemon's socket")
	}
}

func TestRunReplayNode_ReadsItsRunThroughTheDaemon(t *testing.T) {
	home := wingdTestHome(t)
	sock, _ := startAPIDaemon(t, home, nil)
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	backends, release := HostedBackends(paths, sock, nil)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	if err := backends.State.CreateRun(ctx, store.Run{
		ID: "regular", Pipeline: "hosted-memo", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	_, err := RunReplayNode(ctx, paths, backends, "regular", "first", nil)
	if err == nil {
		t.Fatal("a run that is not a replay was replayed")
	}
	if !strings.Contains(err.Error(), "replay") {
		t.Fatalf("err = %v, want the replay guard reached over the daemon", err)
	}
}

func TestWingdAPI_UnknownRouteAnswersUnsupportedWithNoStore(t *testing.T) {
	home := wingdTestHome(t)
	sock, _ := startAPIDaemon(t, home, nil)
	if _, err := os.Stat(PathsAt(home).StateDB()); err == nil {
		t.Fatal("the fixture created a store")
	}

	httpClient := apiHTTPClient(sock)
	defer httpClient.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, apiBaseURL+"/api/v1/no-such-route", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %s, want 404 for a route this build does not serve", resp.Status)
	}
	var body struct {
		Error string `json:"error"`
		Route string `json:"route"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != controller.UnsupportedRouteError || body.Route == "" {
		t.Fatalf("body = %+v, want the unsupported-route answer", body)
	}
}

// safety: stamps a schema requirement no build knows, so opening the store at
// path fails the way an older binary meeting a newer store does.
func requireUnknownFeature(t *testing.T, path string) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(
		`INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES (?, ?, ?)`,
		"a-feature-from-the-future", time.Now().UnixNano(), "v99.0.0",
	); err != nil {
		t.Fatalf("stamp the requirement: %v", err)
	}
	if _, err := store.Open(path); err == nil {
		t.Fatal("the store still opens after an unknown requirement was stamped")
	}
}

func TestWingdAPI_ArtifactRouteFollowsTheConfiguredStore(t *testing.T) {
	probe := func(api *wingdAPI, path string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, apiBaseURL+path, nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		rec := httptest.NewRecorder()
		api.route(rec, req)
		return rec.Code
	}

	home := wingdTestHome(t)
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })

	art, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	withStore := newWingdAPI(runs, art, nil)
	if got := probe(withStore, "/api/v1/artifacts/some-key"); got == http.StatusNotFound {
		t.Fatal("a daemon that configured an artifact store reported its artifact route unsupported")
	}

	without := newWingdAPI(runs, nil, nil)
	if got := probe(without, "/api/v1/artifacts/some-key"); got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 from a daemon with no artifact store", got)
	}
}

// safety: a refusal must not read like a success. The block promises
// "everything else works" and the store it opens is what doctor reports as
// runs that went standalone, so neither may survive a run that is refused.
func TestHostedRun_ARefusedRunLeavesNoBlockAndNoStore(t *testing.T) {
	registerHostedPipelines(t)
	home := wingdTestHome(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv(AllowUnadmittedEnv, "")
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}

	// safety: a daemon that serves no api.sock, so selection cannot ask its
	// health which kind of store fault this is, holding a file that is not a
	// database. Admission is the first thing that answers.
	brokenHome := wingdTestHome(t)
	if err := PathsAt(brokenHome).EnsureRoot(); err != nil {
		t.Fatalf("ensure broken root: %v", err)
	}
	if err := os.WriteFile(PathsAt(brokenHome).StateDB(), []byte("this is not a database"), 0o600); err != nil {
		t.Fatalf("write a non-database: %v", err)
	}
	runs, err := NewHeldRunStore(brokenHome)
	if err != nil {
		t.Fatalf("held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	startWingdCfg(t, wingd.Config{
		Home:    home,
		Version: "test",
		Runs:    runs,
		Sampler: stubSampler{wingd.HostStat{
			TotalCores: 8, TotalMemoryBytes: 64 << 30, FreeMemoryBytes: 64 << 30,
			LoadMeasured: true, MemoryMeasured: true,
		}},
		HeadroomFraction: -1,
	})

	warnings := captureStandaloneWarnings(t)
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	res, err := RunLocal(ctx, paths, Options{Pipeline: "hosted-memo", Admission: testWingdAdmission(home, nil)})
	if err == nil && res != nil && res.Status == "success" {
		t.Fatal("a daemon that cannot read its runs store admitted this run")
	}
	if got := warnings.String(); got != "" {
		t.Fatalf("a refused run printed a standalone block:\n%s", got)
	}
	if _, err := os.Stat(paths.StandaloneStateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s = %v, want no standalone store after a refusal", paths.StandaloneStateDB(), err)
	}
	if _, err := os.Stat(paths.StandaloneDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused run left %s behind", paths.StandaloneDir())
	}
}
