package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestBuildRunInvocation_LogPathFromLocalLogBackend(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	dir := localRunLogDir(localLogs{paths: p}, "run-1")
	if want := p.RunDir("run-1"); dir != want {
		t.Fatalf("localRunLogDir = %q, want %q", dir, want)
	}
	inv := buildRunInvocation(Options{Pipeline: "demo"}, "run-1", dir, nil)
	got, ok := inv["log_path"].(string)
	if !ok {
		t.Fatalf("log_path missing or not a string: %#v", inv["log_path"])
	}
	if got != p.RunDir("run-1") {
		t.Errorf("log_path = %q, want %q", got, p.RunDir("run-1"))
	}
	if !filepath.IsAbs(got) {
		t.Errorf("log_path = %q, want an absolute path", got)
	}
	// The envelope and node logs must live under the advertised
	// directory; that is the whole point of publishing it.
	if d := filepath.Dir(p.EnvelopeLog("run-1")); d != got {
		t.Errorf("envelope log lives in %q, log_path says %q", d, got)
	}
	if d := filepath.Dir(p.NodeLog("run-1", "build")); d != got {
		t.Errorf("node log lives in %q, log_path says %q", d, got)
	}
	if hints, hasHints := inv["hints"].(map[string]string); hasHints {
		if _, buried := hints["log_path"]; buried {
			t.Error("log_path must be a top-level invocation field, not a hint")
		}
	}
}

// A pointer receiver reaches the same Paths as the value form; the
// switch in localRunLogDir must keep answering for both so a future
// decorator author has a case to copy.
func TestLocalRunLogDir_PointerBackend(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	if got, want := localRunLogDir(&localLogs{paths: p}, "run-1"), p.RunDir("run-1"); got != want {
		t.Errorf("localRunLogDir(*localLogs) = %q, want %q", got, want)
	}
}

// SPARKWING_HOME may be relative ("SPARKWING_HOME=.sparkwing sparkwing
// run ..."). A relative log_path is useless to a reader that does not
// share the writer's working directory, so the recorded value is
// absolute regardless.
func TestLocalRunLogDir_RelativeHomeRecordsAbsolutePath(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	t.Setenv("SPARKWING_HOME", "relative-home")

	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if filepath.IsAbs(p.RunDir("run-1")) {
		t.Fatalf("fixture is not exercising the relative case: %q", p.RunDir("run-1"))
	}

	got := localRunLogDir(localLogs{paths: p}, "run-1")
	if !filepath.IsAbs(got) {
		t.Fatalf("log_path = %q, want an absolute path", got)
	}
	if want := filepath.Join(wd, "relative-home", "runs", "run-1"); got != want {
		t.Errorf("log_path = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("recorded log_path does not exist: %v", err)
	}
}

// `logs: {type: filesystem}` writes node logs to local disk just as
// surely as the default localLogs backend does; it simply arrives
// through HTTPLogs wrapping a storage.LogStore. Reporting no log_path
// for those runs sent every reader scraping the stream for output that
// was sitting on their own disk the whole time.
func TestLogPath_FilesystemLogsStoreIsRecorded(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logroot")
	store, err := fs.NewLogStore(root)
	if err != nil {
		t.Fatalf("NewLogStore: %v", err)
	}
	got := localRunLogDir(NewLogStoreBackend(store, nil), "run-1")
	if got == "" {
		t.Fatal("filesystem log store reported no log_path")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("log_path = %q, want an absolute path", got)
	}
	info, err := os.Stat(got)
	if err != nil || !info.IsDir() {
		t.Fatalf("recorded log_path %q is not an existing directory (err=%v)", got, err)
	}
}

// The recorded directory has to be the one the store actually writes
// into. The probe reproduces fs.LogStore's layout rather than asking
// it, so this is the test that keeps the two from drifting apart into
// a truthfully-existing but permanently empty directory.
func TestLogPath_FilesystemLogsStoreMatchesWhereItWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logroot")
	store, err := fs.NewLogStore(root)
	if err != nil {
		t.Fatalf("NewLogStore: %v", err)
	}
	const runID, nodeID = "run-1", "build"
	if err := store.Append(context.Background(), runID, nodeID, []byte(`{"msg":"hello"}`+"\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	dir := localRunLogDir(NewLogStoreBackend(store, nil), runID)
	if dir == "" {
		t.Fatal("filesystem log store reported no log_path")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read recorded log_path: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), nodeID+".") {
			found = true
		}
	}
	if !found {
		t.Errorf("node log is not under the recorded log_path %q; entries=%v", dir, entries)
	}
}

// A run id that the store itself would refuse cannot be advertised as
// a directory either -- the probe must not create a path outside the
// store's root.
func TestLogPath_FilesystemLogsStoreRejectsUnsafeRunID(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logroot")
	store, err := fs.NewLogStore(root)
	if err != nil {
		t.Fatalf("NewLogStore: %v", err)
	}
	for _, bad := range []string{"", "..", "../escape", "a/b"} {
		if dir := localRunLogDir(NewLogStoreBackend(store, nil), bad); dir != "" {
			t.Errorf("run id %q: localRunLogDir = %q, want empty", bad, dir)
		}
	}
}

func TestBuildRunInvocation_NoLogPathWithoutLocalLogs(t *testing.T) {
	inv := buildRunInvocation(Options{Pipeline: "demo"}, "run-1", "", nil)
	if v, ok := inv["log_path"]; ok {
		t.Errorf("log_path must be omitted when the run writes no local logs; got %v", v)
	}
}

func TestLocalRunLogDir_NonLocalBackendsReportNothing(t *testing.T) {
	for name, b := range map[string]LogBackend{
		"log store": NewLogStoreBackend(storage.LogStore(nil), nil),
		"http logs": NewHTTPLogsWithToken("https://controller.example.dev", nil, "tok", nil),
		"nil":       nil,
	} {
		if dir := localRunLogDir(b, "run-1"); dir != "" {
			t.Errorf("%s backend: localRunLogDir = %q, want empty", name, dir)
		}
	}
}

// An unwritable root cannot hold a run directory, so there is nothing
// truthful to advertise.
func TestLocalRunLogDir_UnwritableRootReportsNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(root, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if dir := localRunLogDir(localLogs{paths: Paths{Root: root}}, "run-1"); dir != "" {
		t.Errorf("localRunLogDir = %q, want empty when the dir cannot be created", dir)
	}
}

// The worker and local-trigger paths call Run() directly, so nothing
// has called EnsureRunDir by the time the invocation is built. A run
// that then dies during planning never opens a node log either, which
// is exactly the case where naming an uncreated directory would be a
// lie. Whenever log_path is recorded, the directory exists.
func TestRun_PlanFailureRecordsLogPathThatExists(t *testing.T) {
	ctx := context.Background()
	p := Paths{Root: t.TempDir()}
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// --start-at names a step that does not exist: the run is created,
	// then fails validation before any node dispatches.
	res, err := Run(ctx, LocalBackends(p, st, nil), Options{
		Pipeline: "orch-ok",
		StartAt:  "no-such-step",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("run status = %q, want failed (fixture must fail at plan time)", res.Status)
	}

	run, err := st.GetRun(ctx, res.RunID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	got, _ := run.Invocation["log_path"].(string)
	if got == "" {
		t.Fatalf("no log_path recorded for a local-backend run: %#v", run.Invocation)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("recorded log_path %q does not exist: %v", got, err)
	}
	if !info.IsDir() {
		t.Fatalf("recorded log_path %q is not a directory", got)
	}
	if want := p.RunDir(res.RunID); got != want {
		t.Errorf("log_path = %q, want %q", got, want)
	}
	// Nothing ran, so the directory is empty -- it exists because the
	// invocation build ensured it, not because a node happened to open
	// a log in it.
	entries, err := os.ReadDir(got)
	if err != nil {
		t.Fatalf("read run dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected an empty run dir after a plan failure, got %v", entries)
	}
}
