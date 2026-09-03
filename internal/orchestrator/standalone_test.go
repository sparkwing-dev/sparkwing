package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
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

func TestStandaloneWarning_TextIsFixed(t *testing.T) {
	cases := []struct {
		reason, daemon, sdk, want string
	}{
		{standaloneNoDaemon, "", "v0.41.0", standaloneNoDaemonBlock},
		{standaloneDaemonOlder, "v0.38.2", "v0.41.0", standaloneDaemonOlderBlock},
		{standaloneFloor, "v0.38.2", "v0.37.1", standaloneFloorBlock},
	}
	for _, tc := range cases {
		if got := standaloneWarning(tc.reason, tc.daemon, tc.sdk); got != tc.want {
			t.Errorf("%s block:\ngot  %q\nwant %q", tc.reason, got, tc.want)
		}
	}
	if got := standaloneWarning("", "", ""); got != "" {
		t.Errorf("a hosted run printed %q", got)
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
