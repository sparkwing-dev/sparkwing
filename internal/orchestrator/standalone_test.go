package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type syncWriter struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func captureStandaloneWarnings(t *testing.T) *syncWriter {
	t.Helper()
	out := &syncWriter{}
	previous := standaloneWarningOut
	standaloneWarningOut = out
	t.Cleanup(func() { standaloneWarningOut = previous })
	return out
}

const standaloneNoDaemonBlock = `sparkwing: no admission daemon is running and no sparkwing is installed to host one, so this run is standalone. It cannot see other runs on this machine and they cannot see it, so together they may oversubscribe it. Everything else works.

  to host one
    curl -fsSL https://sparkwing.dev/install.sh | sh
`

const standaloneDaemonOlderBlock = `sparkwing: the admission daemon (v0.38.2) predates this pipeline's SDK (v0.41.0), so this run is standalone. It cannot see other runs on this machine and they cannot see it, so together they may oversubscribe it. Everything else works.

  to update the daemon
    sparkwing update
`

const standaloneFloorBlock = `sparkwing: this pipeline's SDK (v0.37.1) predates what the admission daemon can serve, so this run is standalone. It cannot see other runs on this machine and they cannot see it, so together they may oversubscribe it. Everything else works.

  to update every repo on this machine
    sparkwing repos update --apply

  to update just this repo
    sparkwing repos update --apply --repo .
`

const standaloneDaemonFaultBlock = `sparkwing: the admission daemon (v0.41.0) cannot serve this run's state (bind api.sock: path too long), so this run is standalone. It cannot see other runs on this machine and they cannot see it, so together they may oversubscribe it. Everything else works.

  to see why
    sparkwing daemon status
`

const standaloneForcedBlock = `sparkwing: SPARKWING_ALLOW_UNADMITTED is set, so this run is standalone. It cannot see other runs on this machine and they cannot see it, so together they may oversubscribe it. Everything else works.

  to rejoin the daemon
    unset SPARKWING_ALLOW_UNADMITTED
`

func TestStandaloneWarning_TextIsFixed(t *testing.T) {
	cases := []struct {
		sel  hostedSelection
		sdk  string
		want string
	}{
		{hostedSelection{standalone: standaloneNoDaemon}, "v0.41.0", standaloneNoDaemonBlock},
		{hostedSelection{standalone: standaloneDaemonOlder, daemon: "v0.38.2"}, "v0.41.0", standaloneDaemonOlderBlock},
		{hostedSelection{standalone: standaloneFloor, daemon: "v0.38.2"}, "v0.37.1", standaloneFloorBlock},
		{
			hostedSelection{standalone: standaloneDaemonFault, daemon: "v0.41.0", fault: "bind api.sock: path too long"},
			"v0.41.0", standaloneDaemonFaultBlock,
		},
		{hostedSelection{standalone: standaloneForced, daemon: "v0.41.0"}, "v0.41.0", standaloneForcedBlock},
	}
	for _, tc := range cases {
		if got := standaloneWarning(tc.sel, tc.sdk); got != tc.want {
			t.Errorf("%s block:\ngot  %q\nwant %q", tc.sel.standalone, got, tc.want)
		}
	}
	if got := standaloneWarning(hostedSelection{}, ""); got != "" {
		t.Errorf("a hosted run printed %q", got)
	}
}

func TestStandaloneWarning_NeverDoublesTheParentheses(t *testing.T) {
	block := standaloneWarning(hostedSelection{standalone: standaloneDaemonOlder, daemon: "(devel)"}, "(devel)")
	if strings.Contains(block, "((") || strings.Contains(block, "))") {
		t.Fatalf("block doubles the parentheses a source build's version already carries: %q", block)
	}
	if !strings.Contains(block, "(devel)") {
		t.Fatalf("block dropped the version: %q", block)
	}
	if got := bareVersion(""); got != "unknown" {
		t.Errorf("bareVersion(\"\") = %q, want unknown", got)
	}
}

func TestStandaloneReasonFor_ClassifiesEachBranch(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost), standaloneNoDaemon},
		{fmt.Errorf("x: %w", wingdclient.ErrDaemonHostUnusable), standaloneNoDaemon},
		{fmt.Errorf("x: %w", wingdclient.ErrDaemonHostFailed), standaloneNoDaemon},
		{fmt.Errorf("x: %w", wingdclient.ErrDaemonTooOld), standaloneDaemonOlder},
		{fmt.Errorf("x: %w", wingdclient.ErrDaemonLacksOperation), standaloneDaemonOlder},
		{fmt.Errorf("x: %w", client.ErrControllerLacksRoute), standaloneDaemonOlder},
		{fmt.Errorf("x: %w", wingdclient.ErrProtocolTooOld), standaloneFloor},
		{fmt.Errorf("x: %w", wingdclient.ErrDaemonUnreachable), ""},
		{fmt.Errorf("x: %w", wingdclient.ErrBuildMismatch), ""},
		{errors.New("disk is on fire"), ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := standaloneReasonFor(tc.err); got != tc.want {
			t.Errorf("standaloneReasonFor(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func standaloneRunLocal(t *testing.T, pipeline string) (*Result, Paths, *storeOpenLog, *syncWriter) {
	t.Helper()
	registerHostTestPipelines()
	home := wingdTestHome(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv(AllowUnadmittedEnv, "")
	paths := PathsAt(home)
	opens := countStoreOpens(t)
	warnings := captureStandaloneWarnings(t)

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	res, err := RunLocal(ctx, paths, Options{
		Pipeline:  pipeline,
		Admission: unhostedAdmission(home, &syncWriter{}),
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	return res, paths, opens, warnings
}

func TestRunLocal_StandaloneStoreIsTheOnlyOneOpened(t *testing.T) {
	res, paths, opens, warnings := standaloneRunLocal(t, "host-implicit")
	if res.Status != "success" {
		t.Fatalf("status = %q (%v), want success", res.Status, res.Error)
	}
	if got := opens.opened(paths.StateDB()); got != 0 {
		t.Fatalf("the run opened this machine's shared store %d times, want 0", got)
	}
	if got := opens.opened(paths.StandaloneStateDB()); got != 1 {
		t.Fatalf("the run opened %s %d times, want 1", paths.StandaloneStateDB(), got)
	}
	if _, err := os.Stat(paths.StateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s = %v, want the shared store never created", paths.StateDB(), err)
	}
	if got := warnings.String(); got != standaloneNoDaemonBlock {
		t.Fatalf("stderr block:\ngot  %q\nwant %q", got, standaloneNoDaemonBlock)
	}

	st, err := store.Open(paths.StandaloneStateDB())
	if err != nil {
		t.Fatalf("open standalone store: %v", err)
	}
	defer func() { _ = st.Close() }()
	run, err := st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("read the standalone run row: %v", err)
	}
	on, reason := runStandalone(run)
	if !on || reason != standaloneNoDaemon {
		t.Fatalf("start record standalone=%v reason=%q, want true and %q", on, reason, standaloneNoDaemon)
	}
}

func TestRunLocal_StandaloneRunsAResourcesPin(t *testing.T) {
	res, _, _, warnings := standaloneRunLocal(t, "host-plan-resources")
	if res.Status != "success" {
		t.Fatalf("a .Resources() pin on an empty box = %q (%v), want success", res.Status, res.Error)
	}
	if got := warnings.String(); got != standaloneNoDaemonBlock {
		t.Fatalf("stderr block:\ngot  %q\nwant %q", got, standaloneNoDaemonBlock)
	}
}

func TestOpenStandaloneStore_SharesOneFileUntilItIsRefused(t *testing.T) {
	home := wingdTestHome(t)
	paths := PathsAt(home)

	st, path, discard, err := openStandaloneStore(paths, false)
	if err != nil {
		t.Fatalf("openStandaloneStore: %v", err)
	}
	discard()
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if path != paths.StandaloneStateDB() {
		t.Fatalf("path = %q, want the shared standalone store %q", path, paths.StandaloneStateDB())
	}

	// safety: the shared file now records a requirement this binary does not
	// know, which is the only thing that may send it to a store of its own.
	requireUnknownFeature(t, paths.StandaloneStateDB())

	st, path, discard, err = openStandaloneStore(paths, false)
	if err != nil {
		t.Fatalf("openStandaloneStore after a refusal: %v", err)
	}
	defer discard()
	defer func() { _ = st.Close() }()
	if path != paths.StandaloneSchemaStateDB() {
		t.Fatalf("path = %q, want the per-schema fallback %q", path, paths.StandaloneSchemaStateDB())
	}
}

func TestOpenStandaloneStore_ADryRunLeavesNothingBehind(t *testing.T) {
	home := wingdTestHome(t)
	paths := PathsAt(home)

	st, path, discard, err := openStandaloneStore(paths, true)
	if err != nil {
		t.Fatalf("openStandaloneStore: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	discard()

	if strings.HasPrefix(path, home) {
		t.Fatalf("a dry run wrote %q inside this home", path)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s = %v, want the throwaway store removed", path, err)
	}
	if _, err := os.Stat(paths.StandaloneDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a dry run created %s", paths.StandaloneDir())
	}
}

func TestLocalTriggerBackends_OpensTheStoreTheParentNamed(t *testing.T) {
	home := wingdTestHome(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv(AllowUnadmittedEnv, "")
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}

	parent := filepath.Join(t.TempDir(), "state.db")
	seed, err := store.Open(parent)
	if err != nil {
		t.Fatalf("open the parent's store: %v", err)
	}
	if err := seed.CreateTrigger(context.Background(), store.Trigger{ID: "trg-child", Pipeline: "p"}); err != nil {
		t.Fatalf("seed a trigger: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	t.Setenv(StandaloneStateDBEnv, parent)
	t.Setenv(StandaloneReasonEnv, standaloneDaemonOlder)
	if got := parentStandaloneReason(); got != standaloneDaemonOlder {
		t.Fatalf("parentStandaloneReason() = %q, want the parent's own reason", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	backends, _, release, err := localTriggerBackends(ctx, paths, "")
	if err != nil {
		t.Fatalf("localTriggerBackends: %v", err)
	}
	defer release()
	if _, err := backends.State.GetTrigger(ctx, "trg-child"); err != nil {
		t.Fatalf("the child opened a store its trigger is not in: %v", err)
	}
	if _, err := os.Stat(paths.StandaloneStateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the child derived %s of its own", paths.StandaloneStateDB())
	}
	if _, err := os.Stat(paths.StateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the child opened the shared store %s", paths.StateDB())
	}
}

// safety: the dashboard's trigger consumer claims from the shared store and
// names no store for its child, so the child must land in that same file.
func TestLocalTriggerBackends_DefaultsToTheSharedStore(t *testing.T) {
	home := wingdTestHome(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv(AllowUnadmittedEnv, "")
	t.Setenv(StandaloneStateDBEnv, "")
	t.Setenv(StandaloneReasonEnv, "")
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	seed, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open the shared store: %v", err)
	}
	if err := seed.CreateTrigger(context.Background(), store.Trigger{ID: "trg-consumer", Pipeline: "p"}); err != nil {
		t.Fatalf("seed a trigger: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	backends, _, release, err := localTriggerBackends(ctx, paths, "")
	if err != nil {
		t.Fatalf("localTriggerBackends: %v", err)
	}
	defer release()
	if _, err := backends.State.GetTrigger(ctx, "trg-consumer"); err != nil {
		t.Fatalf("a consumer-dispatched child could not read its trigger: %v", err)
	}
	if _, err := os.Stat(paths.StandaloneStateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the child derived %s instead of the store it was dispatched from", paths.StandaloneStateDB())
	}
}
