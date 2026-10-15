package main

import (
	"context"
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
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type cronsProfileFixture struct {
	url   string
	store *store.Store
}

// safety: a real controller with auth from its own store, and a scratch
// config.yaml pointed at it, so the --profile path is not stubbed out.
func newCronsProfileFixture(t *testing.T) *cronsProfileFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, _, err := st.CreateToken("operator", store.TokenKindUser,
		[]string{controller.ScopeRunsRead, controller.ScopeRunsControl}, 0, time.Now().UTC())
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

func TestCronsInstallAndUninstallProfile_PushesAndRemovesARepository(t *testing.T) {
	f := newCronsProfileFixture(t)
	repo := newCronsControllerRepo(t)

	out := captureStdout(t, func() {
		if err := runIn(t, repo, func(args []string) error { return runSchedules(t.Context(), args) }, "install", "--profile", "prod", "--unpinned", "-o", "pretty"); err != nil {
			t.Errorf("schedules install --profile: %v", err)
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
		if err := runIn(t, repo, func(args []string) error { return runSchedules(t.Context(), args) }, "uninstall", "--profile", "prod", "-o", "pretty"); err != nil {
			t.Errorf("schedules uninstall --profile: %v", err)
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

func TestCronsInstallProfile_RefusesAHeadNoRemoteBranchCarries(t *testing.T) {
	newCronsProfileFixture(t)
	repo := newCronsControllerRepo(t)

	err := runIn(t, repo, func(args []string) error { return runSchedules(t.Context(), args) }, "install", "--profile", "prod", "-o", "pretty")
	if err == nil {
		t.Fatal("a commit no remote branch carries was pushed anyway")
	}
	for _, want := range []string{"no origin branch", "--unpinned"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestCronsInstallProfile_PushesOnceARemoteBranchCarriesHeadAndWarnsWhenDirty(t *testing.T) {
	f := newCronsProfileFixture(t)
	repo := newCronsControllerRepo(t)
	gitInRepo(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out string
	warning := captureStderr(t, func() {
		out = captureStdout(t, func() {
			if err := runIn(t, repo, func(args []string) error { return runSchedules(t.Context(), args) }, "install", "--profile", "prod", "-o", "pretty"); err != nil {
				t.Errorf("schedules install --profile: %v", err)
			}
		})
	})
	if !strings.Contains(out, "pushed") {
		t.Errorf("install reported no push:\n%s", out)
	}
	if !strings.Contains(warning, "uncommitted edits") {
		t.Errorf("a dirty working tree drew no warning:\n%s", warning)
	}
	rows, err := (&crons.Service{Store: f.store}).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].LockedRef == "" {
		t.Fatalf("rows = %+v, want one pinned at the pushed commit", rows)
	}
}

func TestCronsInstallProfile_PlainPrintsBareNames(t *testing.T) {
	newCronsProfileFixture(t)
	repo := newCronsControllerRepo(t)

	out := captureStdout(t, func() {
		if err := runIn(t, repo, func(args []string) error { return runSchedules(t.Context(), args) }, "install", "--profile", "prod", "--unpinned", "-o", "plain"); err != nil {
			t.Errorf("schedules install --profile -o plain: %v", err)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 || lines[0] != "acme/widgets/cluster-sweep" {
		t.Errorf("plain output = %q, want one bare schedule name", out)
	}
}

func TestSelectDeclaredForPushTrimsTheNamesItIsGiven(t *testing.T) {
	declared := []crons.Declared{
		declaredControllerEntry("nightly", "default", "0 3 * * *"),
		declaredControllerEntry("sweep", "quick", "*/5 * * * *"),
	}
	got, err := selectDeclaredForPush(declared, []string{" nightly ", "", "  ", "sweep/quick\t"}, "/repo")
	if err != nil {
		t.Fatalf("selectDeclaredForPush: %v", err)
	}
	if len(got) != 2 || got[0].Pipeline != "nightly" || got[1].Name != "quick" {
		t.Fatalf("selected %+v, want both entries", got)
	}
	if _, err := selectDeclaredForPush(declared, []string{" nope "}, "/repo"); err == nil {
		t.Error("a name the repo does not declare was accepted")
	}
}

func gitInRepo(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}
