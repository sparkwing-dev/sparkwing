//go:build unix

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestPinnedEngineColdStartKeepsNewerStoreController(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an older-schema engine and real pipeline")
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	build := t.TempDir()
	overlay, err := seededBundleOverlay(build)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatal(err)
	}
	var document struct{ Replace map[string]string }
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "internal", "store", "store.go")
	body, err = os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := store.ExpectedSchemaVersion() - 1
	old := strings.Replace(string(body), "const expectedSchemaVersion = "+strconv.Itoa(store.ExpectedSchemaVersion()), "const expectedSchemaVersion = "+strconv.Itoa(oldSchema), 1)
	if old == string(body) {
		t.Fatal("older-schema fixture did not change its supported schema")
	}
	replacement := filepath.Join(build, "store.go")
	if err := os.WriteFile(replacement, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	document.Replace[source] = replacement
	body, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, body, 0o600); err != nil {
		t.Fatal(err)
	}
	engine := filepath.Join(build, "older-engine")
	command := procenv.ControlCommandContext(t.Context(), os.Environ(), "go", "build", "-overlay", overlay, "-o", engine, "./cmd/sparkwing")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("older engine build: %v\n%s", err, output)
	}

	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(home, "secrets.key"))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(home, "no-bus"))
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"Cloud is unavailable"}}`))
	}))
	t.Cleanup(cloud.Close)
	if err := os.WriteFile(filepath.Join(home, "missing.yaml"), []byte("profiles:\n  offline:\n    controller: {url: "+cloud.URL+", token: swu_test}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	offlineStopDaemon(t, home)
	if _, err := wingdclient.QueryLocalController(t.Context(), home); !errors.Is(err, wingdclient.ErrNoDaemon) {
		t.Fatalf("controller was not cold: %v", err)
	}
	repo := initSnapshotRepo(t)
	writeSDKFixture(t, filepath.Join(repo, ".sparkwing"), `package main
import("context"; sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type hello struct{sw.Base}
func(hello) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{sw.Job(p,"hello",func(context.Context)error{return nil});return nil}
func main(){sw.Register("hello",func()sw.Pipeline[sw.NoInputs]{return hello{}});sw.Main()}
`)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "pipeline fixture")
	t.Chdir(repo)
	for _, route := range []string{"local", "burst"} {
		t.Run(route, func(t *testing.T) {
			offlineStopDaemon(t, home)
			if err := wingdclient.Stop(t.Context(), wingdclient.Options{Home: home, RetireRunner: true}); err != nil && !errors.Is(err, wingdclient.ErrNoDaemon) {
				t.Fatal(err)
			}
			if _, err := wingdclient.QueryLocalController(t.Context(), home); !errors.Is(err, wingdclient.ErrNoDaemon) {
				t.Fatalf("controller was not cold: %v", err)
			}
			args := []string{"run", "hello", "--route", route, "-o", "plain"}
			if route == "burst" {
				args = append(args, "--profile", "offline")
			}
			inv, err := clisurface.Parse("sparkwing", args)
			if err != nil {
				t.Fatal(err)
			}
			completed := errors.New("engine process completed")
			err = dispatchRunWithEngine(inv, func(_ string, beforeExec func() error) error {
				if err := beforeExec(); err != nil {
					return err
				}
				started, startErr := wingdclient.QueryLocalController(t.Context(), home)
				if startErr != nil || !started.Running || started.PID <= 0 {
					t.Fatalf("invoking controller was not ready before handoff: %+v %v", started, startErr)
				}
				command := procenv.ControlCommandContext(t.Context(), os.Environ(), engine, args...)
				command.Dir = repo
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("schema %d engine failed against schema %d store: %v\n%s", oldSchema, store.ExpectedSchemaVersion(), err, output)
				}
				if route == "burst" && !strings.Contains(string(output), "Cloud controller is unreachable; running locally.") {
					t.Fatalf("burst did not exercise local fallback: %s", output)
				}
				status, err := wingdclient.QueryLocalController(t.Context(), home)
				if err != nil || !status.Running {
					t.Fatalf("installed controller did not remain ready: %+v %v", status, err)
				}
				t.Logf("older engine run succeeded: %s", output)
				return completed
			})
			if !errors.Is(err, completed) {
				t.Fatal(err)
			}
		})
	}
}

func TestEngineHandoffRefusesNewerStoreBeforeDelegation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(home, "secrets.key"))
	state, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	newer := store.ExpectedSchemaVersion() + 1
	if _, err := db.Exec("UPDATE sparkwing_schema_version SET version = ?", newer); err != nil {
		t.Fatal(err)
	}
	offlineStopDaemon(t, home)
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, ".sparkwing/main.go", "package main\nfunc main(){}\n", 0o600)
	t.Chdir(repo)
	for _, tc := range []struct {
		name   string
		args   []string
		shared bool
	}{
		{"local", nil, true},
		{"burst", []string{"--route", "burst", "--profile", "prod"}, true},
		{"standalone", []string{"--allow-unadmitted"}, false},
		{"cloud", []string{"--route", "cloud", "--profile", "prod"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := clisurface.Parse("sparkwing", append([]string{"run", "hello"}, tc.args...))
			if err != nil {
				t.Fatal(err)
			}
			completed := errors.New("selected engine reached")
			err = dispatchRunWithEngine(inv, func(_ string, beforeExec func() error) error {
				if err := beforeExec(); err != nil {
					return err
				}
				return completed
			})
			if tc.shared {
				if err == nil || !strings.Contains(err.Error(), "newer than this binary") {
					t.Fatalf("unsupported store reached engine: %v", err)
				}
				message := err.Error()
				upgrade, backup := strings.Index(message, "upgrade the installed CLI (`sparkwing update`)"), strings.Index(message, "backup restore loses newer runs")
				if upgrade < 0 || backup < upgrade {
					t.Fatalf("refusal does not lead with the upgrade remedy before data-loss advice: %s", message)
				}
			} else if !errors.Is(err, completed) {
				t.Fatalf("isolated placement opened shared store: %v", err)
			}
			var version int
			if err := db.QueryRow("SELECT version FROM sparkwing_schema_version").Scan(&version); err != nil || version != newer {
				t.Fatalf("refused store changed: %d %v", version, err)
			}
		})
	}
}
