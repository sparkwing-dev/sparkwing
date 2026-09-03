package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func setCoordinatedControllerProfile(t *testing.T, controllerURL string) {
	t.Helper()
	writeInnerProfiles(t, fmt.Sprintf(`
profiles:
  remote:
    controller: { url: %s }
    secrets: { type: controller }
    state: { type: controller }
    cache: { type: controller }
    logs: { type: controller }
`, controllerURL))
	t.Setenv("SPARKWING_PROFILE", "remote")
}

func TestCoordinatedChildSurfaces_LocalOnlyNeverOpensProfileBackends(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "remote surface reached", http.StatusInternalServerError)
	}))
	defer srv.Close()
	setCoordinatedControllerProfile(t, srv.URL)
	t.Setenv("SPARKWING_LOCAL_ONLY", "1")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	secretsDir := filepath.Join(home, ".config", "sparkwing")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "secrets.env"), []byte("TOKEN=local-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	source, art, logs, err := coordinatedChildSurfaces(context.Background(), "gate")
	if err != nil {
		t.Fatalf("coordinatedChildSurfaces: %v", err)
	}
	if art != nil || logs != nil {
		t.Fatalf("local-only child opened remote surfaces: cache=%T logs=%T", art, logs)
	}
	value, _, err := source.Read("TOKEN")
	if err != nil || value != "local-token" {
		t.Fatalf("local secret = %q, %v; want local-token", value, err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("local-only child made %d controller request(s)", got)
	}
}

func TestCoordinatedChildSurfaces_ProfileBackendsRemainRemoteWithoutLocalOnly(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case r.URL.Path == "/api/v1/secrets/TOKEN":
			fmt.Fprintln(w, `{"value":"remote-token","masked":true}`)
		case r.URL.Path == "/bin/probe":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/api/v1/logs/run/node":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	setCoordinatedControllerProfile(t, srv.URL)
	t.Setenv("SPARKWING_LOCAL_ONLY", "")

	source, art, logs, err := coordinatedChildSurfaces(context.Background(), "gate")
	if err != nil {
		t.Fatalf("coordinatedChildSurfaces: %v", err)
	}
	value, masked, err := source.Read("TOKEN")
	if err != nil || value != "remote-token" || !masked {
		t.Fatalf("remote secret = %q, masked=%v, err=%v", value, masked, err)
	}
	if found, err := art.Has(context.Background(), "probe"); err != nil || found {
		t.Fatalf("remote cache probe = %v, %v; want missing", found, err)
	}
	nodeLog, err := logs.OpenNodeLog(context.Background(), "run", "node", nil)
	if err != nil {
		t.Fatalf("open remote log: %v", err)
	}
	nodeLog.Log("info", "remote log")
	if err := nodeLog.Close(); err != nil {
		t.Fatalf("close remote log: %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("normal child made %d controller request(s), want secrets, cache, and logs", got)
	}
}

func TestCoordinatedChildSurfaces_UnreachableProfileStillFailsWithoutLocalOnly(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	controllerURL := srv.URL
	srv.Close()
	setCoordinatedControllerProfile(t, controllerURL)
	t.Setenv("SPARKWING_LOCAL_ONLY", "")

	source, art, logs, err := coordinatedChildSurfaces(context.Background(), "gate")
	if err != nil {
		t.Fatalf("opening remote clients: %v", err)
	}
	if source == nil || art == nil || logs == nil {
		t.Fatalf("normal child did not retain its profile: secrets=%T cache=%T logs=%T", source, art, logs)
	}
	if _, _, err := source.Read("TOKEN"); err == nil || !strings.Contains(err.Error(), controllerURL) {
		t.Fatalf("unreachable secret error = %v", err)
	}
}

func TestCoordinatedLogBackend_NoLogsSurfaceKeepsTheRunsOwnFiles(t *testing.T) {
	ctx := context.Background()

	for name, prof := range map[string]*profile.Profile{
		"no profile at all": nil,
		"sqlite state only": {
			Name:  "laptop",
			State: &backends.Spec{Type: backends.TypeSQLite, Path: filepath.Join(t.TempDir(), "state.db")},
		},
	} {
		got, err := coordinatedLogBackend(ctx, prof)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != nil {
			t.Errorf("%s: opened a %T log backend; want nil so the caller keeps the run's local files", name, got)
		}
	}
}

func TestCoordinatedLogBackend_UnopenableSurfaceFailsTheNode(t *testing.T) {
	_, err := coordinatedLogBackend(context.Background(), &profile.Profile{
		Name:  "shared-team",
		State: &backends.Spec{Type: backends.TypeSQLite, Path: filepath.Join(t.TempDir(), "state.db")},
		Logs:  &backends.Spec{Type: backends.TypeGCS, Bucket: "team"},
	})
	if err == nil {
		t.Fatal("an unopenable logs surface did not fail the node")
	}
	for _, want := range []string{"shared-team", "gcs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestStartRunLoopback_MirroredRunTeesChildWritesToBothStores(t *testing.T) {
	paths := newInternalPaths(t)

	canonicalStore, err := store.Open(filepath.Join(t.TempDir(), "canonical.db"))
	if err != nil {
		t.Fatalf("open canonical store: %v", err)
	}
	t.Cleanup(func() { _ = canonicalStore.Close() })
	srv := httptest.NewServer(controller.New(canonicalStore, quietTestLogger()).Handler())
	t.Cleanup(srv.Close)
	canonical := client.NewWithToken(srv.URL, nil, "")

	mirror, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open mirror store: %v", err)
	}
	t.Cleanup(func() { _ = mirror.Close() })

	ctx := context.Background()
	const runID = "run-mirrored"
	backends := RemoteBackends(canonical, nil, nil, nil, store.DefaultConcurrencyLease)
	backends.State = newMirrorStateBackend(backends.State, mirror, quietTestLogger())
	if err := backends.State.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "mirrored", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	loopback, err := startRunLoopback(&Options{State: canonical, RunID: runID}, backends, quietTestLogger())
	if err != nil {
		t.Fatalf("startRunLoopback: %v", err)
	}
	t.Cleanup(loopback.Close)

	child := client.NewWithToken(loopback.url, nil, loopback.token)
	if err := child.CreateNode(ctx, store.Node{RunID: runID, NodeID: "n", Status: "pending"}); err != nil {
		t.Fatalf("child CreateNode: %v", err)
	}
	if err := child.FinishNode(ctx, runID, "n", "success", "", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("child FinishNode: %v", err)
	}

	for name, st := range map[string]*store.Store{"canonical": canonicalStore, "mirror": mirror} {
		n, gerr := st.GetNode(ctx, runID, "n")
		if gerr != nil {
			t.Fatalf("%s store: read node: %v", name, gerr)
		}
		if n.Outcome != "success" {
			t.Errorf("%s store: node outcome = %q, want success", name, n.Outcome)
		}
		if string(n.Output) != `{"ok":true}` {
			t.Errorf("%s store: node output = %s", name, n.Output)
		}
	}
}
