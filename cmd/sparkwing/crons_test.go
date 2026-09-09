package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func (l *recordingLauncher) Active(_ context.Context, runID string, _ time.Duration) (bool, error) {
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
			GOOS:       "linux",
			Home:       unitRoot,
			ConfigHome: filepath.Join(unitRoot, ".config"),
			Binary:     "/usr/local/bin/sparkwing",
			PathEnv:    "/usr/bin:/bin",
			LogPath:    filepath.Join(paths.Root, cronsLogFile),
			UID:        1000,
			Exec:       func(string, ...string) (string, error) { return "", nil },
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

func TestCronsInstallArmsTheRepoAndInstallsTheTimer(t *testing.T) {
	_, _ = cronsTestHome(t)
	repo := cronsTestRepo(t, cronsMinutelyRepo)

	out := captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	if !strings.Contains(out, "armed "+filepath.Base(repo)+"/every-minute") {
		t.Fatalf("install output:\n%s", out)
	}
	if !strings.Contains(out, "timer: ") || !strings.Contains(out, crontimer.TimerName) {
		t.Errorf("install did not report the timer:\n%s", out)
	}

	list := captureStdout(t, func() {
		if err := runCronsList([]string{"-o", "pretty"}); err != nil {
			t.Fatalf("crons list: %v", err)
		}
	})
	if !strings.Contains(list, "every-minute") || !strings.Contains(list, "armed") {
		t.Fatalf("list output:\n%s", list)
	}
}

func TestCronsInstallOnARepoDeclaringNoScheduleInstallsNoTimer(t *testing.T) {
	_, _ = cronsTestHome(t)
	repo := cronsTestRepo(t, `pipelines:
  - name: manual
    entrypoint: Manual
`)
	out := captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	if !strings.Contains(out, "nothing to arm") {
		t.Fatalf("install output:\n%s", out)
	}
	if strings.Contains(out, crontimer.TimerName) {
		t.Errorf("a repo with no schedule installed a timer:\n%s", out)
	}
}

func TestCronsStatusOnAnEmptyHomeIsHealthy(t *testing.T) {
	_, _ = cronsTestHome(t)
	out := captureStdout(t, func() {
		if err := runCronsStatus([]string{"-o", "pretty"}); err != nil {
			t.Fatalf("crons status: %v", err)
		}
	})
	if !strings.Contains(out, "0 armed") || !strings.Contains(out, "nothing is armed here") {
		t.Fatalf("status output:\n%s", out)
	}
}

func TestCronsStatusExitsNonZeroWhenAnArmedHostHasNoTimer(t *testing.T) {
	_, _ = cronsTestHome(t)
	repo := cronsTestRepo(t, cronsMinutelyRepo)
	captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	var err error
	out := captureStdout(t, func() { err = runCronsStatus([]string{"-o", "pretty"}) })
	if err == nil {
		t.Fatalf("status exited zero with an armed schedule and no timer:\n%s", out)
	}
	if code := exitCodeFor(err); code == 0 {
		t.Errorf("exit code = %d, want non-zero", code)
	}
	if !strings.Contains(out, "1 armed") {
		t.Errorf("status output:\n%s", out)
	}
}

func TestCronsTickFiresADueScheduleThroughTheLauncher(t *testing.T) {
	_, launcher := cronsTestHome(t)
	repo := cronsTestRepo(t, cronsMinutelyRepo)
	captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})

	when := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Minute).Format(time.RFC3339)
	dry := captureStdout(t, func() {
		if err := runCronsTick([]string{"--dry-run", "--sw-now", when, "-o", "pretty"}); err != nil {
			t.Fatalf("crons tick --dry-run: %v", err)
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
			t.Fatalf("crons tick: %v", err)
		}
	})
	if len(launcher.launched) != 1 {
		t.Fatalf("tick launched %v", launcher.launched)
	}
	if !strings.Contains(out, launcher.launched[0]) {
		t.Errorf("tick output does not name the run:\n%s", out)
	}

	show := captureStdout(t, func() {
		if err := runCronsShow([]string{"every-minute", "-o", "pretty"}); err != nil {
			t.Fatalf("crons show: %v", err)
		}
	})
	if !strings.Contains(show, "fired") || !strings.Contains(show, launcher.launched[0]) {
		t.Fatalf("show output:\n%s", show)
	}
}

func TestCronsRunLaunchesRegardlessOfCadence(t *testing.T) {
	_, launcher := cronsTestHome(t)
	repo := cronsTestRepo(t, `pipelines:
  - name: yearly
    entrypoint: Yearly
    on:
      schedule:
        cron: "@yearly"
        where: local
`)
	captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	captureStdout(t, func() {
		if err := runCronsPause([]string{"yearly", "-o", "pretty"}); err != nil {
			t.Fatalf("crons pause: %v", err)
		}
	})
	out := captureStdout(t, func() {
		if err := runCronsRun([]string{"yearly", "-o", "pretty"}); err != nil {
			t.Fatalf("crons run: %v", err)
		}
	})
	if len(launcher.launched) != 1 {
		t.Fatalf("crons run launched %v", launcher.launched)
	}
	for _, want := range []string{"submitted", "runs logs --run", "runs cancel --run"} {
		if !strings.Contains(out, want) {
			t.Errorf("run output is missing %q:\n%s", want, out)
		}
	}
	resumed := captureStdout(t, func() {
		if err := runCronsResume([]string{"yearly", "-o", "pretty"}); err != nil {
			t.Fatalf("crons resume: %v", err)
		}
	})
	if !strings.Contains(resumed, "armed") {
		t.Errorf("resume output:\n%s", resumed)
	}
}

func TestCronsListAndNextEmitNDJSON(t *testing.T) {
	_, _ = cronsTestHome(t)
	for _, body := range []string{cronsMinutelyRepo, `pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        cron: "0 3 * * *"
        where: local
`} {
		repo := cronsTestRepo(t, body)
		captureStdout(t, func() {
			if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "--no-timer", "-o", "pretty"}); err != nil {
				t.Fatalf("crons install: %v", err)
			}
		})
	}

	list := captureStdout(t, func() {
		if err := runCronsList([]string{"-o", "json"}); err != nil {
			t.Fatalf("crons list: %v", err)
		}
	})
	rows := decodeNDJSONLines[crons.Row](t, list)
	if len(rows) != 2 {
		t.Fatalf("list emitted %d row(s):\n%s", len(rows), list)
	}
	for _, r := range rows {
		if r.Display == "" || r.State != crons.StateArmed {
			t.Errorf("row: %+v", r)
		}
	}

	next := captureStdout(t, func() {
		if err := runCronsNext([]string{"--count", "3", "-o", "json"}); err != nil {
			t.Fatalf("crons next: %v", err)
		}
	})
	instants := decodeNDJSONLines[cronsUpcoming](t, next)
	if len(instants) != 3 {
		t.Fatalf("next emitted %d instant(s):\n%s", len(instants), next)
	}
	for i := 1; i < len(instants); i++ {
		if instants[i].At.Before(instants[i-1].At) {
			t.Fatalf("instants are not in time order: %+v", instants)
		}
	}
}

func TestCronsShowEmitsOneJSONRecord(t *testing.T) {
	_, _ = cronsTestHome(t)
	repo := cronsTestRepo(t, cronsMinutelyRepo)
	captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	out := captureStdout(t, func() {
		if err := runCronsShow([]string{"every-minute", "-o", "json"}); err != nil {
			t.Fatalf("crons show: %v", err)
		}
	})
	report := oneJSONRecord[cronsShowReport](t, out)
	if report.Pipeline != "every-minute" || report.Cron != "* * * * *" {
		t.Fatalf("show record: %+v", report.Row)
	}
}

func TestCronsVerbsEmitOneJSONRecordPerLine(t *testing.T) {
	_, _ = cronsTestHome(t)
	repo := cronsTestRepo(t, cronsMinutelyRepo)

	install := captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "--no-timer", "-o", "json"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	if armed := oneJSONRecord[cronsInstallReport](t, install); armed.Armed != 1 {
		t.Errorf("install record: %+v", armed)
	}

	status := captureStdout(t, func() { _ = runCronsStatus([]string{"-o", "json"}) })
	if health := oneJSONRecord[crons.Health](t, status); health.Armed != 1 {
		t.Errorf("status record: %+v", health)
	}

	paused := captureStdout(t, func() {
		if err := runCronsPause([]string{"every-minute", "-o", "json"}); err != nil {
			t.Fatalf("crons pause: %v", err)
		}
	})
	if row := oneJSONRecord[crons.Row](t, paused); row.State != crons.StatePaused {
		t.Errorf("pause record: %+v", row)
	}

	ran := captureStdout(t, func() {
		if err := runCronsRun([]string{"every-minute", "-o", "json"}); err != nil {
			t.Fatalf("crons run: %v", err)
		}
	})
	if launched := oneJSONRecord[cronsRunReport](t, ran); launched.RunID == "" {
		t.Errorf("run record: %+v", launched)
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
		err = runCronsInstall([]string{"--repo", repo, "--no-prove", "-o", "json"})
	})
	if err != nil {
		t.Fatalf("a platform with no OS timer failed the install: %v", err)
	}
	report := oneJSONRecord[cronsInstallReport](t, out)
	if report.Armed != 1 {
		t.Errorf("install record: %+v", report)
	}
	if !strings.Contains(report.TimerSkip, "crons tick") {
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
		err = runCronsInstall([]string{"--repo", repo, "--no-prove", "-o", "json"})
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

func TestCronsPlainPrintsThePrimaryValueOnly(t *testing.T) {
	_, launcher := cronsTestHome(t)
	repo := cronsTestRepo(t, cronsMinutelyRepo)
	install := captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "--no-timer", "-o", "plain"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	if install != filepath.Base(repo)+"/every-minute\n" {
		t.Errorf("install plain = %q, want the armed name alone", install)
	}

	ran := captureStdout(t, func() {
		if err := runCronsRun([]string{"every-minute", "-o", "plain"}); err != nil {
			t.Fatalf("crons run: %v", err)
		}
	})
	if len(launcher.launched) != 1 || ran != launcher.launched[0]+"\n" {
		t.Errorf("run plain = %q, want the run id alone", ran)
	}
}

// safety: one JSON line per verb is what makes a piped verb safe to cut with
// head and jq.
func oneJSONRecord[T any](t *testing.T, body string) T {
	t.Helper()
	records := decodeNDJSONLines[T](t, body)
	if len(records) != 1 {
		t.Fatalf("output carries %d record(s), want exactly one:\n%s", len(records), body)
	}
	return records[0]
}

func TestCronsResolveNamesTheCandidatesOrSaysNothingIsArmed(t *testing.T) {
	_, _ = cronsTestHome(t)
	err := runCronsShow([]string{"nope", "-o", "pretty"})
	if err == nil {
		t.Fatal("show of an unarmed name succeeded")
	}
	if !strings.Contains(err.Error(), "crons list") {
		t.Errorf("error %q does not point at the listing", err)
	}

	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(t.TempDir(), name)
		if merr := os.MkdirAll(dir, 0o755); merr != nil {
			t.Fatal(merr)
		}
		writeRepoFile(t, filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), cronsMinutelyRepo)
		captureStdout(t, func() {
			if ierr := runCronsInstall([]string{"--repo", dir, "--no-prove", "--no-timer", "-o", "pretty"}); ierr != nil {
				t.Fatalf("crons install: %v", ierr)
			}
		})
	}
	err = runCronsShow([]string{"every-minute", "-o", "pretty"})
	if !errors.Is(err, crons.ErrAmbiguousName) {
		t.Fatalf("error = %v, want ErrAmbiguousName", err)
	}
	for _, want := range []string{"alpha/every-minute", "beta/every-minute"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestCronsUninstallRemovesTheRowsAndTheTimer(t *testing.T) {
	_, _ = cronsTestHome(t)
	repo := cronsTestRepo(t, cronsMinutelyRepo)
	captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	out := captureStdout(t, func() {
		if err := runCronsUninstall([]string{"--repo", repo, "-o", "pretty"}); err != nil {
			t.Fatalf("crons uninstall: %v", err)
		}
	})
	if !strings.Contains(out, "disarmed 1 schedule(s)") {
		t.Fatalf("uninstall output:\n%s", out)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("uninstall did not report the timer going:\n%s", out)
	}
	list := captureStdout(t, func() {
		if err := runCronsList([]string{"--all", "-o", "pretty"}); err != nil {
			t.Fatalf("crons list: %v", err)
		}
	})
	if !strings.Contains(list, "no schedules are armed here") {
		t.Fatalf("list after uninstall:\n%s", list)
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
	st, err := store.Open(paths.StateDB())
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

func TestCronLaunchReportsAConsumerThatWillNotStart(t *testing.T) {
	launcher, st := cronsRealLauncher(t)
	prior := cronsEnsureConsumer
	cronsEnsureConsumer = func(string) error { return errors.New("consumer did not take the queue lock") }
	t.Cleanup(func() { cronsEnsureConsumer = prior })

	sched := store.CronSchedule{ID: "crn_x", RepoPath: cronsTestRepo(t, cronsMinutelyRepo), Pipeline: "every-minute"}
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
	sched := store.CronSchedule{ID: "crn_x", RepoPath: cronsTestRepo(t, cronsMinutelyRepo), Pipeline: "every-minute"}
	runID, err := launcher.Launch(ctx, sched, time.Now())
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	fresh, err := launcher.Active(ctx, runID, time.Hour)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !fresh {
		t.Error("a run queued a moment ago is not active")
	}
	stale, err := launcher.Active(ctx, runID, time.Nanosecond)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if stale {
		t.Error("a pending run older than the catch-up window still counts as active")
	}
}

func TestCronsPauseAndResumeNameTheScheduleTheWayListDoes(t *testing.T) {
	_, _ = cronsTestHome(t)
	repo := cronsTestRepo(t, `pipelines:
  - name: sweep
    entrypoint: Sweep
    on:
      schedule:
        - name: quick
          cron: "*/5 * * * *"
          where: local
`)
	captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-prove", "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	want := filepath.Base(repo) + "/sweep/quick"

	paused := captureStdout(t, func() {
		if err := runCronsPause([]string{"sweep/quick", "-o", "pretty"}); err != nil {
			t.Fatalf("crons pause: %v", err)
		}
	})
	if !strings.Contains(paused, want+" is paused") {
		t.Errorf("pause named the schedule as %q, want %q:\n%s", strings.TrimSpace(paused), want, paused)
	}
	resumed := captureStdout(t, func() {
		if err := runCronsResume([]string{"sweep/quick", "-o", "pretty"}); err != nil {
			t.Fatalf("crons resume: %v", err)
		}
	})
	if !strings.Contains(resumed, want+" is armed") {
		t.Errorf("resume named the schedule as %q, want %q:\n%s", strings.TrimSpace(resumed), want, resumed)
	}
}

func TestCronsLockAndUnlockDoNotAdvertiseProfile(t *testing.T) {
	for _, cmd := range []Command{cmdCronsLock, cmdCronsUnlock} {
		for _, flag := range cmd.Flags {
			if flag.Name == "profile" {
				t.Errorf("%s advertises --profile, which it refuses", cmd.Path)
			}
		}
	}
	if !strings.Contains(cmdCrons.Description, "every verb but tick, lock and unlock") {
		t.Errorf("the crons group still claims every verb but tick takes --profile:\n%s", cmdCrons.Description)
	}
}
