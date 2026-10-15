package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type recordingLauncher struct {
	launched []string
	active   map[string]bool
}

func (l *recordingLauncher) Launch(_ context.Context, s store.CronSchedule, due time.Time) (string, error) {
	id := "run-" + s.Pipeline + "-" + due.UTC().Format("150405")
	l.launched = append(l.launched, id)
	return id, nil
}

func (l *recordingLauncher) Active(_ context.Context, _ store.CronSchedule, runID string, _ time.Duration) (bool, error) {
	return l.active[runID], nil
}

func cronsTestHome(t *testing.T) (home string, launcher *recordingLauncher) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("SPARKWING_HOME", home)

	launcher = &recordingLauncher{active: map[string]bool{}}
	priorLauncher := cronsLauncher
	cronsLauncher = func(*store.Store, orchestrator.Paths) crons.Launcher { return launcher }
	t.Cleanup(func() { cronsLauncher = priorLauncher })

	unitRoot := t.TempDir()
	priorHost := cronsTimerHost
	cronsTimerHost = func(paths orchestrator.Paths) (crontimer.Host, error) {
		return crontimer.Host{
			GOOS:          "linux",
			Home:          unitRoot,
			ConfigHome:    filepath.Join(unitRoot, ".config"),
			Binary:        cronsTestTimerBinary(),
			SparkwingHome: paths.Root,
			Version:       "v1.2.3",
			PathEnv:       "/usr/bin:/bin",
			LogPath:       filepath.Join(paths.Root, cronsLogFile),
			UID:           1000,
			Exec:          func(context.Context, string, ...string) (string, error) { return "", nil },
		}, nil
	}
	t.Cleanup(func() { cronsTimerHost = priorHost })
	return home, launcher
}

func cronsTestRepo(t *testing.T, body string) string {
	t.Helper()
	repo := t.TempDir()
	writeRepoFile(t, filepath.Join(repo, ".sparkwing", "sparkwing.yaml"), body)
	return repo
}

const cronsMinutelyRepo = `pipelines:
  - name: every-minute
    entrypoint: EveryMinute
    on:
      schedule:
        cron: "* * * * *"
        where: local
`

func TestCronsInstallOnARepoDeclaringNoScheduleInstallsNoTimer(t *testing.T) {
	_, _ = cronsTestHome(t)
	repo := cronsTestRepo(t, `pipelines:
  - name: manual
    entrypoint: Manual
`)
	out := captureStdout(t, func() {
		if err := runIn(t, repo, runCronsInstall, "--no-prove", "-o", "pretty"); err != nil {
			t.Fatalf("schedules install: %v", err)
		}
	})
	if !strings.Contains(out, "nothing to arm") {
		t.Fatalf("install output:\n%s", out)
	}
	if strings.Contains(out, "sparkwing-crons") {
		t.Errorf("a repo with no schedule installed a timer:\n%s", out)
	}
}

func TestCronsTickFiresADueScheduleThroughTheLauncher(t *testing.T) {
	_, launcher := cronsTestHome(t)
	repo := cronsTestRepo(t, cronsMinutelyRepo)
	captureStdout(t, func() {
		if err := runIn(t, repo, runCronsInstall, "--no-prove", "--no-timer", "-o", "pretty"); err != nil {
			t.Fatalf("schedules install: %v", err)
		}
	})

	when := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Minute).Format(time.RFC3339)
	dry := captureStdout(t, func() {
		if err := runCronsTick([]string{"--dry-run", "--sw-now", when, "-o", "pretty"}); err != nil {
			t.Fatalf("__tick --dry-run: %v", err)
		}
	})
	if !strings.Contains(dry, "1 fired") {
		t.Fatalf("dry run output:\n%s", dry)
	}
	if len(launcher.launched) != 0 {
		t.Fatalf("a dry run launched %v", launcher.launched)
	}

	out := captureStdout(t, func() {
		if err := runCronsTick([]string{"--sw-now", when, "-o", "pretty"}); err != nil {
			t.Fatalf("__tick: %v", err)
		}
	})
	if len(launcher.launched) != 1 {
		t.Fatalf("tick launched %v", launcher.launched)
	}
	if !strings.Contains(out, launcher.launched[0]) {
		t.Errorf("tick output does not name the run:\n%s", out)
	}

	session, release, err := openCrons("")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rows, err := session.svc.List(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("schedules: %+v %v", rows, err)
	}
	_, fires, err := session.svc.Show(t.Context(), rows[0].ID, 10)
	if err != nil || len(fires) != 1 || fires[0].RunID != launcher.launched[0] {
		t.Fatalf("history: %+v %v", fires, err)
	}
}

func TestCronsInstallReportsWhatItArmedOnAPlatformWithNoTimer(t *testing.T) {
	_, _ = cronsTestHome(t)
	prior := cronsTimerHost
	cronsTimerHost = func(paths orchestrator.Paths) (crontimer.Host, error) {
		host, err := prior(paths)
		host.GOOS = "windows"
		return host, err
	}
	t.Cleanup(func() { cronsTimerHost = prior })

	repo := cronsTestRepo(t, cronsMinutelyRepo)
	var err error
	out := captureStdout(t, func() {
		err = runIn(t, repo, runCronsInstall, "--no-prove", "-o", "json")
	})
	if err != nil {
		t.Fatalf("a platform with no OS timer failed the install: %v", err)
	}
	report := oneJSONRecord[cronsInstallReport](t, out)
	if report.Armed != 1 {
		t.Errorf("install record: %+v", report)
	}
	if !strings.Contains(report.TimerSkip, "__tick") {
		t.Errorf("timer_skipped = %q, want the hint about driving the tick", report.TimerSkip)
	}
}

func TestCronsInstallRendersTheReportWhenTheTimerStepFails(t *testing.T) {
	_, _ = cronsTestHome(t)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	prior := cronsTimerHost
	cronsTimerHost = func(paths orchestrator.Paths) (crontimer.Host, error) {
		host, err := prior(paths)
		host.ConfigHome = blocked
		return host, err
	}
	t.Cleanup(func() { cronsTimerHost = prior })

	repo := cronsTestRepo(t, cronsMinutelyRepo)
	var err error
	out := captureStdout(t, func() {
		err = runIn(t, repo, runCronsInstall, "--no-prove", "-o", "json")
	})
	if err == nil {
		t.Fatal("a timer that could not be written exited zero")
	}
	report := oneJSONRecord[cronsInstallReport](t, out)
	if report.Armed != 1 {
		t.Errorf("the report did not survive the timer failure: %+v", report)
	}
	if report.TimerError == "" {
		t.Errorf("timer_error is empty: %+v", report)
	}
}

func oneJSONRecord[T any](t *testing.T, body string) T {
	t.Helper()
	records := decodeNDJSONLines[T](t, body)
	if len(records) != 1 {
		t.Fatalf("output carries %d record(s), want exactly one:\n%s", len(records), body)
	}
	return records[0]
}

func TestCronsUninstallRemovesTheRowsAndTheTimer(t *testing.T) {
	_, _ = cronsTestHome(t)
	repo := cronsTestRepo(t, cronsMinutelyRepo)
	captureStdout(t, func() {
		if err := runIn(t, repo, runCronsInstall, "--no-prove", "-o", "pretty"); err != nil {
			t.Fatalf("schedules install: %v", err)
		}
	})
	out := captureStdout(t, func() {
		if err := runIn(t, repo, runCronsUninstall, "-o", "pretty"); err != nil {
			t.Fatalf("schedules uninstall: %v", err)
		}
	})
	if !strings.Contains(out, "disarmed 1 schedule(s)") {
		t.Fatalf("uninstall output:\n%s", out)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("uninstall did not report the timer going:\n%s", out)
	}
	session, release, err := openCrons("")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rows, err := session.svc.List(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("uninstall left schedules: %+v %v", rows, err)
	}
}

func TestCronsTickRejectsAMalformedNow(t *testing.T) {
	_, _ = cronsTestHome(t)
	err := runCronsTick([]string{"--sw-now", "yesterday"})
	if err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("error = %v, want the RFC3339 shape named", err)
	}
}

// safety: one record per line is what makes a piped listing safe to cut with head and jq.
func decodeNDJSONLines[T any](t *testing.T, body string) []T {
	t.Helper()
	var out []T
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("line %q is not one JSON record: %v", line, err)
		}
		out = append(out, v)
	}
	return out
}

// safety: this is the launcher scheduled runs actually go through, against a
// scratch home, so the queue and the consumer are the real ones.
func cronsRealLauncher(t *testing.T) (cronLauncher, *store.Store) {
	t.Helper()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})
	return cronLauncher{store: st, paths: paths}, st
}

// safety: the launcher weighs what the checkout declares before it queues a
// run, so a launch test needs a checkout that answers describe. The stub the
// detached-submit tests use does, and declares no risk.
func cronsBuildableRepo(t *testing.T) string {
	t.Helper()
	repo := cronsTestRepo(t, cronsMinutelyRepo)
	writeRepoFile(t, filepath.Join(repo, ".sparkwing", "go.mod"), "module submitfixture\n\ngo 1.22\n")
	writeRepoFile(t, filepath.Join(repo, ".sparkwing", "main.go"), strings.ReplaceAll(submitFixtureSource, "fixture", "every-minute"))
	if out, err := exec.Command("git", "-C", repo, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("initialize buildable cron checkout: %v: %s", err, out)
	}
	return repo
}

func TestCronLaunchReportsAConsumerThatWillNotStart(t *testing.T) {
	launcher, st := cronsRealLauncher(t)
	prior := cronsEnsureConsumer
	cronsEnsureConsumer = func(string) error { return errors.New("consumer did not take the queue lock") }
	t.Cleanup(func() { cronsEnsureConsumer = prior })

	sched := store.CronSchedule{ID: "crn_x", RepoPath: cronsBuildableRepo(t), Pipeline: "every-minute"}
	runID, err := launcher.Launch(context.Background(), sched, time.Now())
	if err == nil {
		t.Fatal("a launch with no consumer to run it reported success")
	}
	if runID != "" {
		t.Errorf("run id = %q, want none so the tick records a failure", runID)
	}
	for _, want := range []string{"stays queued", "consumer did not take the queue lock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
	runs, lerr := st.ListRuns(context.Background(), store.RunFilter{})
	if lerr != nil {
		t.Fatalf("ListRuns: %v", lerr)
	}
	if len(runs) != 1 {
		t.Fatalf("the queued run did not survive the failure: %+v", runs)
	}
}

func TestCronLauncherStopsCountingAPendingRunOnceItIsStale(t *testing.T) {
	launcher, _ := cronsRealLauncher(t)
	prior := cronsEnsureConsumer
	cronsEnsureConsumer = func(string) error { return nil }
	t.Cleanup(func() { cronsEnsureConsumer = prior })

	ctx := context.Background()
	sched := store.CronSchedule{ID: "crn_x", RepoPath: cronsBuildableRepo(t), Pipeline: "every-minute"}
	runID, err := launcher.Launch(ctx, sched, time.Now())
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	fresh, err := launcher.Active(ctx, sched, runID, time.Hour)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !fresh {
		t.Error("a run queued a moment ago is not active")
	}
	stale, err := launcher.Active(ctx, sched, runID, time.Nanosecond)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if stale {
		t.Error("a pending run older than the catch-up window still counts as active")
	}
}

func cronsTestTimerBinary() string {
	if runtime.GOOS == "windows" {
		return filepath.VolumeName(os.TempDir()) + "/usr/local/bin/sparkwing"
	}
	return "/usr/local/bin/sparkwing"
}

func TestCronsUninstallStillDisarmsAfterTheProjectFilesAreGone(t *testing.T) {
	_, _ = cronsTestHome(t)
	repo := cronsTestRepo(t, cronsMinutelyRepo)
	captureStdout(t, func() {
		if err := runIn(t, repo, runCronsInstall, "--no-prove", "--no-timer", "-o", "pretty"); err != nil {
			t.Fatalf("schedules install: %v", err)
		}
	})
	if err := os.Remove(filepath.Join(repo, ".sparkwing", "sparkwing.yaml")); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runIn(t, repo, runCronsUninstall, "-o", "pretty"); err != nil {
			t.Fatalf("schedules uninstall with the markers gone: %v", err)
		}
	})
	if !strings.Contains(out, "disarmed 1 schedule(s)") {
		t.Fatalf("uninstall output:\n%s", out)
	}
}
