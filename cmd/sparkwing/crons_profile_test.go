package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type cronsProfileFixture struct {
	url   string
	store *store.Store
}

// safety: a real controller with auth from its own store, and a scratch
// profiles.yaml pointed at it, so the --profile path is not stubbed out.
func newCronsProfileFixture(t *testing.T) *cronsProfileFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, _, err := st.CreateToken("operator", store.TokenKindUser,
		[]string{controller.ScopeRunsRead, controller.ScopeRunsWrite}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	writeProfilesFixture(t, fmt.Sprintf(
		"profiles:\n  prod:\n    controller: { url: %s, token: %s }\n", srv.URL, raw))
	return &cronsProfileFixture{url: srv.URL, store: st}
}

func (f *cronsProfileFixture) push(t *testing.T) {
	t.Helper()
	svc := &crons.Service{Store: f.store}
	if _, err := svc.ArmPushed(context.Background(), crons.ArmPush{
		RepoURL: "https://github.com/acme/widgets.git",
		Branch:  "main",
		SHA:     "0123456789abcdef0123456789abcdef01234567",
		Entries: []crons.Declared{declaredControllerEntry("nightly", "default", "0 3 * * *")},
	}); err != nil {
		t.Fatalf("seed the controller: %v", err)
	}
}

func TestCronsListProfile_RendersTheControllersRows(t *testing.T) {
	f := newCronsProfileFixture(t)
	f.push(t)

	out := captureStdout(t, func() {
		if err := runCrons([]string{"list", "--profile", "prod", "-o", "pretty"}); err != nil {
			t.Errorf("crons list --profile: %v", err)
		}
	})
	if !strings.Contains(out, "acme/widgets/nightly") {
		t.Errorf("listing does not name the controller's schedule:\n%s", out)
	}
	if !strings.Contains(out, "0 3 * * *") {
		t.Errorf("listing does not carry the cadence:\n%s", out)
	}
}

func TestCronsShowProfile_RendersOneRow(t *testing.T) {
	f := newCronsProfileFixture(t)
	f.push(t)

	out := captureStdout(t, func() {
		if err := runCrons([]string{"show", "acme/widgets/nightly", "--profile", "prod", "-o", "json"}); err != nil {
			t.Errorf("crons show --profile: %v", err)
		}
	})
	var row map[string]any
	if err := json.Unmarshal([]byte(out), &row); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if row["where"] != store.CronWhereController {
		t.Errorf("where = %v, want controller", row["where"])
	}
	if row["git_branch"] != "main" {
		t.Errorf("git_branch = %v, want main", row["git_branch"])
	}
}

func TestCronsStatusProfile_ReportsTheControllerLoop(t *testing.T) {
	f := newCronsProfileFixture(t)
	f.push(t)

	out := captureStdout(t, func() {
		// safety: a controller that has never ticked is unhealthy while
		// something is armed, which is the exit code an operator reads.
		_ = runCrons([]string{"status", "--profile", "prod", "-o", "pretty"})
	})
	if !strings.Contains(out, crons.ControllerTimerDetail) {
		t.Errorf("status does not name the controller loop:\n%s", out)
	}
}

func TestCronsPauseProfile_PausesOnTheController(t *testing.T) {
	f := newCronsProfileFixture(t)
	f.push(t)

	out := captureStdout(t, func() {
		if err := runCrons([]string{"pause", "acme/widgets/nightly", "--profile", "prod", "-o", "pretty"}); err != nil {
			t.Errorf("crons pause --profile: %v", err)
		}
	})
	if !strings.Contains(out, "is paused") {
		t.Errorf("pause did not report the new state:\n%s", out)
	}
	rows, err := (&crons.Service{Store: f.store}).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !rows[0].Paused {
		t.Error("the controller's row is not paused")
	}
}

func TestCronsInstallAndUninstallProfile_PushesAndRemovesARepository(t *testing.T) {
	f := newCronsProfileFixture(t)
	repo := newCronsControllerRepo(t)

	out := captureStdout(t, func() {
		if err := runCrons([]string{"install", "--profile", "prod", "--repo", repo, "--follow", "-o", "pretty"}); err != nil {
			t.Errorf("crons install --profile: %v", err)
		}
	})
	if !strings.Contains(out, "pushed") {
		t.Errorf("install reported no push:\n%s", out)
	}
	if !strings.Contains(out, "skipped") {
		t.Errorf("install did not report the host's own entry:\n%s", out)
	}

	svc := &crons.Service{Store: f.store}
	rows, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("controller holds %d schedules, want the one controller entry", len(rows))
	}
	if rows[0].Pipeline != "cluster-sweep" {
		t.Errorf("pushed %q, want the where: controller entry", rows[0].Pipeline)
	}
	if rows[0].LockedRef != "" {
		t.Errorf("locked_ref = %q, want empty under --follow", rows[0].LockedRef)
	}
	if rows[0].GitBranch == "" {
		t.Error("the push recorded no branch")
	}

	out = captureStdout(t, func() {
		if err := runCrons([]string{"uninstall", "--profile", "prod", "--repo", repo, "-o", "pretty"}); err != nil {
			t.Errorf("crons uninstall --profile: %v", err)
		}
	})
	if !strings.Contains(out, "disarmed 1") {
		t.Errorf("uninstall reported nothing removed:\n%s", out)
	}
	rows, err = svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("controller still holds %d schedules", len(rows))
	}
}

func TestCronsLockProfile_SaysThePinMovesWithThePush(t *testing.T) {
	newCronsProfileFixture(t)
	err := runCrons([]string{"lock", "nightly", "--profile", "prod"})
	if err == nil || !strings.Contains(err.Error(), "crons install --profile") {
		t.Fatalf("crons lock --profile: err = %v, want the push named as the way to re-pin", err)
	}
}

func declaredControllerEntry(pipeline, name, cron string) crons.Declared {
	entry := crons.Declared{Pipeline: pipeline, Name: name}
	entry.Trigger.Name = name
	entry.Trigger.Cron = cron
	entry.Trigger.Where = store.CronWhereController
	return entry
}

// safety: a checkout declaring one controller schedule and one host schedule,
// with an origin the push can name.
func newCronsControllerRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cfgDir := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	config := `pipelines:
  - name: cluster-sweep
    entrypoint: ClusterSweep
    on:
      schedule:
        - cron: "0 4 * * *"
          where: controller
  - name: host-sweep
    entrypoint: HostSweep
    on:
      schedule:
        - cron: "0 5 * * *"
          where: local
`
	if err := os.WriteFile(filepath.Join(cfgDir, "sparkwing.yaml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"remote", "add", "origin", "https://github.com/acme/widgets.git"},
		{"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-q", "-m", "declare schedules"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return root
}
