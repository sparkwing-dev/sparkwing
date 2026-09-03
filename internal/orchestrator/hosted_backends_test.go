package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
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

func countStoreOpens(t *testing.T) *atomic.Int32 {
	t.Helper()
	var opens atomic.Int32
	previous := openStateStoreFromSpec
	openStateStoreFromSpec = func(ctx context.Context, spec backends.Spec, lookup storeurl.ProfileLookup) (storage.StateStore, error) {
		opens.Add(1)
		return previous(ctx, spec, lookup)
	}
	t.Cleanup(func() { openStateStoreFromSpec = previous })
	return &opens
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

func TestHostedSelection_FallsBackWhenTheDaemonServesNoAPI(t *testing.T) {
	registerHostedPipelines(t)
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	paths := PathsAt(home)
	opens := countStoreOpens(t)

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
	if got := opens.Load(); got != 1 {
		t.Fatalf("the run opened the store %d times, want 1 on the direct path", got)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "does not serve this run's state") {
		t.Fatalf("no fallback line naming the reason; got %q", joined)
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
	got, reason := selectHostedAPI(ctx, testWingdAdmission(home, nil))
	if got != "" {
		t.Fatalf("socket = %q, want the direct path", got)
	}
	if !strings.Contains(reason, "api.sock") {
		t.Fatalf("reason = %q, want it to name the socket", reason)
	}
}

func TestHostedSelection_SkippedWhenUnadmittedIsAllowed(t *testing.T) {
	home := wingdTestHome(t)
	startAPIDaemon(t, home, nil)
	t.Setenv(AllowUnadmittedEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	got, reason := selectHostedAPI(ctx, testWingdAdmission(home, nil))
	if got != "" {
		t.Fatalf("socket = %q, want the direct path", got)
	}
	if !strings.Contains(reason, AllowUnadmittedEnv) {
		t.Fatalf("reason = %q, want it to name %s", reason, AllowUnadmittedEnv)
	}
}

func TestHostedRun_UnusableDaemonStoreFailsTheRun(t *testing.T) {
	registerHostedPipelines(t)
	home := wingdTestHome(t)
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	// safety: a directory where the store file belongs is the cheapest open
	// failure that survives every retry, which is what a wedged store looks
	// like to the run.
	if err := os.MkdirAll(filepath.Join(paths.StateDB()), 0o755); err != nil {
		t.Fatalf("wedge the store: %v", err)
	}
	startAPIDaemon(t, home, nil)

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	started := time.Now()
	res, err := RunLocal(ctx, paths, Options{
		Pipeline:  "hosted-memo",
		Admission: testWingdAdmission(home, nil),
	})
	if err == nil && res != nil && res.Status == "success" {
		t.Fatal("the run succeeded against a store the daemon cannot open")
	}
	if elapsed := time.Since(started); elapsed >= wingdTestWait {
		t.Fatalf("the run took %s, so it hung rather than failing", elapsed)
	}
	message := ""
	switch {
	case err != nil:
		message = err.Error()
	case res != nil && res.Error != nil:
		message = res.Error.Error()
	}
	if !strings.Contains(message, "admission daemon") {
		t.Fatalf("failure = %q, want it to name the admission daemon", message)
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
