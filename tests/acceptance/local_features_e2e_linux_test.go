//go:build linux

package acceptance_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"
)

type localFeatureController struct {
	t                              *testing.T
	ctx                            context.Context
	cli, root, home, repo, markers string
	env                            []string
}

func newLocalFeatureController(t *testing.T) *localFeatureController {
	t.Helper()
	cli := os.Getenv("SPARKWING_LOCAL_E2E_BINARY")
	if cli == "" {
		t.Skip("opt-in real-process scenarios: see tests/acceptance/LOCAL_FEATURES.md")
	}
	if !filepath.IsAbs(cli) {
		t.Fatal("SPARKWING_LOCAL_E2E_BINARY must be absolute")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	user := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	t.Cleanup(cancel)
	f := &localFeatureController{t: t, ctx: ctx, cli: cli, root: root, home: filepath.Join(user, "state"), repo: filepath.Join(user, "repo"), markers: filepath.Join(user, "markers")}
	for _, key := range []string{"PATH", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "ASDF_DATA_DIR", "PLAYWRIGHT_BROWSERS_PATH"} {
		if value, ok := os.LookupEnv(key); ok {
			f.env = append(f.env, key+"="+value)
		}
	}
	cache := exec.CommandContext(ctx, "go", "env", "GOCACHE", "GOMODCACHE", "GOPATH")
	output, err := cache.Output()
	if err != nil {
		t.Fatal(err)
	}
	paths := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(paths) != 3 {
		t.Fatalf("go cache paths: expected three lines, got %d", len(paths))
	}
	for i, value := range paths {
		f.env = append(f.env, []string{"GOCACHE=", "GOMODCACHE=", "GOPATH="}[i]+value)
	}
	if os.Getenv("PLAYWRIGHT_BROWSERS_PATH") == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		f.env = append(f.env, "PLAYWRIGHT_BROWSERS_PATH="+filepath.Join(cache, "ms-playwright"))
	}
	config := filepath.Join(user, "config.yaml")
	for _, path := range []string{f.home, f.repo, f.markers} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	featureWrite(t, config, "dashboard: {address: '127.0.0.1:0'}\nadmission: {budget: ignore-external}\n")
	f.env = append(f.env, "HOME="+user, "XDG_CONFIG_HOME="+filepath.Join(user, "config"), "SPARKWING_HOME="+f.home, "SPARKWING_CONFIG="+config, "SPARKWING_SECRETS_KEY_FILE="+filepath.Join(user, "key"), "LOCAL_FEATURE_DIR="+f.markers, "GOWORK=off", "GOFLAGS=-p=2", "GOMAXPROCS=2", "TMPDIR="+os.TempDir())
	t.Cleanup(func() {
		for _, name := range []string{"running", "child", "session"} {
			featureWrite(t, filepath.Join(f.markers, name+".release"), "")
		}
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		cmd := exec.CommandContext(cleanup, f.cli, "daemon", "stop", "-o", "json")
		cmd.Env = f.env
		cmd.Dir = f.repo
		if body, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("controller cleanup: %v: %s", err, body)
		}
		if t.Failed() {
			logs, _ := filepath.Glob(filepath.Join(f.home, "runners", "*", "runner.log"))
			for _, path := range logs {
				body, _ := os.ReadFile(path)
				t.Logf("runner log: %s", body)
			}
		}
	})
	return f
}

func featureWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *localFeatureController) command(args ...string) []byte {
	f.t.Helper()
	cmd := exec.CommandContext(f.ctx, f.cli, append(args, "-o", "json")...)
	cmd.Env = f.env
	cmd.Dir = f.repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	var exit *exec.ExitError
	liveStatus := len(args) >= 2 && args[0] == "runs" && args[1] == "status" && errors.As(err, &exit) && exit.ExitCode() == 3
	if err != nil && !liveStatus {
		f.t.Fatalf("%s: %v: %s %s", strings.Join(args, " "), err, output, stderr.String())
	}
	return output
}

func (f *localFeatureController) fixture() {
	f.t.Helper()
	dir := filepath.Join(f.repo, ".sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(f.root, "tests/acceptance/testdata/localfeatures/main.go"))
	if err != nil {
		f.t.Fatal(err)
	}
	featureWrite(f.t, filepath.Join(dir, "main.go"), string(source))
	featureWrite(f.t, filepath.Join(dir, "go.mod"), fmt.Sprintf("module localfeatures\n\ngo 1.26.0\n\nrequire github.com/sparkwing-dev/sparkwing/sparkwing v0.70.0\nreplace github.com/sparkwing-dev/sparkwing/sparkwing => %s\n", strconv.Quote(filepath.Join(f.root, "sparkwing"))))
	featureWrite(f.t, filepath.Join(dir, "sparkwing.yaml"), "pipelines:\n  - {name: lifecycle, entrypoint: Lifecycle}\n  - {name: child, entrypoint: Child}\n  - {name: host-exec, entrypoint: HostExec}\n  - {name: cancellation, entrypoint: Cancellation}\n")
	cmd := exec.CommandContext(f.ctx, "go", "-C", dir, "mod", "tidy")
	cmd.Env = f.env
	if output, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("fixture tidy: %v: %s", err, output)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=Acceptance", "-c", "user.email=acceptance@example.invalid", "-c", "commit.gpgsign=false", "commit", "--no-verify", "-qm", "fixture"}} {
		cmd := testgit.Command("git", append([]string{"-C", f.repo}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			f.t.Fatalf("fixture git: %v: %s", err, output)
		}
	}
}

func (f *localFeatureController) submit(name string) string {
	f.t.Helper()
	var receipt struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(f.command("run", name, "--sw-detached", "--sw-no-update"), &receipt); err != nil || receipt.RunID == "" {
		f.t.Fatalf("run receipt: %+v %v", receipt, err)
	}
	return receipt.RunID
}

func (f *localFeatureController) await(label string, condition func() bool) {
	f.t.Helper()
	for !condition() {
		select {
		case <-f.ctx.Done():
			f.t.Fatalf("await %s: %v", label, f.ctx.Err())
		// sleepcheck:external-boundary real CLI processes and filesystem markers need a paced poll
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (f *localFeatureController) pid(name string) localhostwire.LocalRunnerInstance {
	f.t.Helper()
	var identity localhostwire.LocalRunnerInstance
	f.await(name, func() bool {
		raw, err := os.ReadFile(filepath.Join(f.markers, name))
		if err != nil {
			return false
		}
		identity.PID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			f.t.Fatal(err)
		}
		identity.ProcessBirth, err = procgroup.ProcessBirth(identity.PID)
		if err != nil {
			f.t.Fatal(err)
		}
		return true
	})
	f.t.Cleanup(func() {
		if alive, _ := wingd.LocalRunnerProcessAlive(identity); alive {
			p, _ := os.FindProcess(identity.PID)
			_ = p.Signal(syscall.SIGKILL)
			f.t.Errorf("test-owned child %s leaked", name)
		}
	})
	return identity
}

func (f *localFeatureController) alive(identity localhostwire.LocalRunnerInstance) bool {
	f.t.Helper()
	alive, err := wingd.LocalRunnerProcessAlive(identity)
	if err != nil {
		f.t.Fatal(err)
	}
	return alive
}

func (f *localFeatureController) success(run string) {
	f.t.Helper()
	f.await("successful run "+run, func() bool {
		var status struct {
			Status string `json:"status"`
		}
		body := f.command("runs", "status", "--run", run)
		if err := json.Unmarshal(body, &status); err != nil {
			f.t.Fatal(err)
		}
		if slices.Contains([]string{"failed", "cancelled"}, status.Status) {
			f.t.Fatalf("run did not survive: %s", body)
		}
		return status.Status == "success"
	})
}

func TestLocalControllerFeatureE2E(t *testing.T) {
	t.Run("dashboard", func(t *testing.T) {
		f := newLocalFeatureController(t)
		cmd := exec.CommandContext(f.ctx, "node", filepath.Join(f.root, "web/scripts/local-controller-e2e.mjs"), f.cli)
		cmd.Env = f.env
		cmd.Dir = filepath.Join(f.root, "web")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("headless browser: %v: %s", err, output)
		} else {
			t.Log(string(output))
		}
	})
	t.Run("lifecycle", func(t *testing.T) {
		f := newLocalFeatureController(t)
		f.fixture()
		run := f.submit("lifecycle")
		running, child := f.pid("running.pid"), f.pid("child.pid")
		waiting := f.pid("waiting.pid")
		registry, err := wingdclient.NewLocalRunnerClient(f.home, "")
		if err != nil {
			t.Fatal(err)
		}
		runner, err := registry.Active(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		before := f.command("daemon", "status")
		var old, next struct {
			PID       int
			Restarted bool
		}
		if err := json.Unmarshal(before, &old); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(f.command("daemon", "restart", "--force"), &next); err != nil {
			t.Fatal(err)
		}
		if !next.Restarted || next.PID == old.PID {
			t.Fatalf("controller did not restart: %+v %+v", old, next)
		}
		if !f.alive(running) || !f.alive(child) || !f.alive(waiting) || !f.alive(runner) {
			t.Fatal("restart killed running work or its runner")
		}
		featureWrite(t, filepath.Join(f.markers, "running.release"), "")
		featureWrite(t, filepath.Join(f.markers, "child.release"), "")
		f.success(run)
		got, err := os.ReadFile(filepath.Join(f.markers, "awaited"))
		if err != nil || string(got) != "child-finished" {
			t.Fatalf("waiting owner lost child result: %q %v", got, err)
		}
		f.command("daemon", "stop")
		f.await("runner retired", func() bool { return !f.alive(runner) })
		logs, _ := filepath.Glob(filepath.Join(f.home, "runners", "*", "runner.log"))
		for _, path := range logs {
			body, _ := os.ReadFile(path)
			if strings.Contains(string(body), "api.sock") {
				t.Fatalf("runner lost controller before retirement: %s", body)
			}
		}
	})
	t.Run("host-exec", func(t *testing.T) {
		f := newLocalFeatureController(t)
		f.fixture()
		run := f.submit("host-exec")
		background := f.pid("background.pid")
		f.await("command returned", func() bool { _, err := os.Stat(filepath.Join(f.markers, "command-ended")); return err == nil })
		if !f.alive(background) {
			t.Fatal("background child died with command")
		}
		// safety: cross-run cancellation must leave the other session's children alive.
		cancel := f.submit("cancellation")
		child, grandchild := f.pid("cancel.child"), f.pid("cancel.grandchild")
		f.command("runs", "cancel", "--run", cancel)
		f.await("cancelled descendants gone", func() bool { return !f.alive(child) && !f.alive(grandchild) })
		if !f.alive(background) {
			t.Fatal("cancelling another run killed the held background child")
		}
		featureWrite(t, filepath.Join(f.markers, "session.release"), "")
		f.success(run)
		f.await("background child ended with session", func() bool { return !f.alive(background) })
		logs := f.command("runs", "logs", "--run", run, "--job", "mask", "--no-events", "--follow")
		if bytes.Contains(logs, []byte("local-feature")) || bytes.Contains(logs, []byte("boundary-secret")) {
			t.Fatal("secret leaked across output cut")
		}
		if bytes.Count(logs, []byte("mask-end")) != 2 || bytes.Count(logs, []byte("***")) != 2 || len(logs) < 400000 {
			t.Fatalf("large stdout/stderr were lost: bytes=%d end-markers=%d", len(logs), bytes.Count(logs, []byte("mask-end")))
		}
	})
	t.Run("schema-backup", func(t *testing.T) {
		f := newLocalFeatureController(t)
		cmd := exec.CommandContext(f.ctx, f.cli, "secrets", "set", "--secret", "PRESERVED", "-o", "json")
		cmd.Env = f.env
		cmd.Dir = f.repo
		cmd.Stdin = strings.NewReader("private-configured-value")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("configure secret: %v %s", err, output)
		}
		f.command("daemon", "stop")
		path := filepath.Join(f.home, "state.db")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		var sealed string
		if err := db.QueryRow("SELECT value FROM secrets WHERE name='PRESERVED'").Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{
			`CREATE TABLE team_build_trust (
    team TEXT NOT NULL REFERENCES teams(name) PRIMARY KEY,
    trust_local_builds BIGINT NOT NULL DEFAULT 0 CHECK(trust_local_builds IN (0,1))
)`,
			"DROP TABLE github_actions_dispatches", "DROP TABLE github_actions_approvals",
			"UPDATE sparkwing_schema_version SET version=34,baseline_digest='187573d27274e9bf1796a84d502b39ac9e8c0cb7730e41143a1e4f586d5a6012'",
		} {
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 3 {
			old := fmt.Sprintf("%s.schema-%d-fixture.bak", path, 30+i)
			if err := os.WriteFile(old, body, 0o600); err != nil {
				t.Fatal(err)
			}
			stamp := time.Unix(int64(i+1), 0)
			if err := os.Chtimes(old, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
		f.command("secrets", "list")
		f.command("daemon", "stop")
		backups, err := filepath.Glob(path + ".schema-*.bak")
		if err != nil || len(backups) != 2 {
			t.Fatalf("retained backups=%d: %v", len(backups), err)
		}
		if !slices.Contains(backups, path+".schema-32-fixture.bak") {
			t.Fatal("newest earlier backup was pruned")
		}
		for _, backup := range backups {
			if strings.Contains(backup, "fixture") {
				continue
			}
			info, err := os.Stat(backup)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("backup permissions: %v %v", info, err)
			}
			featureCheckStore(t, backup, 34, sealed)
		}
		featureCheckStore(t, path, 37, sealed)
	})
}

func featureCheckStore(t *testing.T, path string, want int, sealed string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var value, integrity string
	if err := db.QueryRow("SELECT version FROM sparkwing_schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT value FROM secrets WHERE name='PRESERVED'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if version != want || value != sealed || integrity != "ok" {
		t.Fatalf("store evidence: schema=%d want=%d secret-preserved=%t integrity=%s", version, want, value == sealed, integrity)
	}
}
