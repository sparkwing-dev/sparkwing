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

// recordingLauncher stands in for the detached submission path, so a tick can
// be exercised without a consumer, a daemon, or a compiled pipeline.
type recordingLauncher struct {
	launched []string
	active   map[string]bool
}

func (l *recordingLauncher) Launch(_ context.Context, s store.CronSchedule, due time.Time) (string, error) {
	id := "run-" + s.Pipeline + "-" + due.UTC().Format("150405")
	l.launched = append(l.launched, id)
	return id, nil
}

func (l *recordingLauncher) Active(_ context.Context, runID string) (bool, error) {
	return l.active[runID], nil
}

// cronsTestHome points every crons verb at a temporary home, a fake service
// manager, and a launcher that records instead of running.
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
      schedule: "* * * * *"
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
      schedule: "@yearly"
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
      schedule: "0 3 * * *"
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
		if r.Name == "" || r.State != crons.StateArmed {
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
	var report cronsShowReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode show json: %v\n%s", err, out)
	}
	if report.Pipeline != "every-minute" || report.Cron != "* * * * *" {
		t.Fatalf("show record: %+v", report.Row)
	}
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

// decodeNDJSONLines insists on one complete record per line, which is what
// makes a piped listing safe to cut with head and jq.
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
