package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
)

const cronsTwoSidedRepo = `pipelines:
  - name: sweep
    entrypoint: Sweep
    on:
      schedule:
        - name: quick
          cron: "*/15 * * * *"
          where: local
          args:
            depth: shallow
        - name: cluster
          cron: "0 4 * * *"
          where: controller
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        cron: "0 3 * * *"
        where: local
`

// safety: pinning copies whatever the compiler names, so a stub binary
// exercises the whole arm without building Go for every case.
func cronsFakeProver(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	prior := cronsProver
	cronsProver = func(noProve bool) crons.Prover {
		if noProve {
			return nil
		}
		return func(_ context.Context, repoRoot, pipeline string) (crons.Proof, error) {
			path := filepath.Join(dir, filepath.Base(repoRoot)+"-"+pipeline)
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				return crons.Proof{}, err
			}
			return crons.Proof{Binary: path, Digest: "digest-" + pipeline}, nil
		}
	}
	t.Cleanup(func() { cronsProver = prior })
}

func cronsTestGitRepo(t *testing.T, body string) string {
	t.Helper()
	repo := cronsTestRepo(t, body)
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "tester@example.invalid"},
		{"config", "user.name", "tester"},
		{"add", "."},
		{"commit", "--quiet", "-m", "declare the schedules"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "commit.gpgSign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return repo
}

func cronsGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo, "-c", "commit.gpgSign=false"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCronsInstallPinsLocalEntriesAndReportsTheControllerOnes(t *testing.T) {
	_, _ = cronsTestHome(t)
	cronsFakeProver(t)
	repo := cronsTestGitRepo(t, cronsTwoSidedRepo)
	base := filepath.Base(repo)
	head := cronsGit(t, repo, "rev-parse", "HEAD")

	out := captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	for _, want := range []string{
		"armed " + base + "/sweep/quick (pinned at " + head[:7],
		"armed " + base + "/nightly (pinned at " + head[:7],
		"skipped " + base + "/sweep/cluster: it fires from the controller",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("install output is missing %q:\n%s", want, out)
		}
	}

	list := captureStdout(t, func() {
		if err := runCronsList([]string{"-o", "pretty"}); err != nil {
			t.Fatalf("crons list: %v", err)
		}
	})
	if !strings.Contains(list, "LOCK") || !strings.Contains(list, head[:7]) {
		t.Fatalf("list does not carry the LOCK column:\n%s", list)
	}
	if strings.Contains(list, "cluster") {
		t.Errorf("a controller entry was armed here:\n%s", list)
	}

	cronsGit(t, repo, "commit", "--quiet", "--allow-empty", "-m", "move past the pin")
	ahead := captureStdout(t, func() {
		if err := runCronsList([]string{"-o", "pretty"}); err != nil {
			t.Fatalf("crons list: %v", err)
		}
	})
	if !strings.Contains(ahead, head[:7]+" ahead") {
		t.Fatalf("list does not report the checkout moving past the pin:\n%s", ahead)
	}

	status := captureStdout(t, func() { _ = runCronsStatus([]string{"-o", "pretty"}) })
	if !strings.Contains(status, "2 locked") || !strings.Contains(status, "crons install") {
		t.Fatalf("status does not count the locks or name the remedy:\n%s", status)
	}
}

func TestCronsInstallOnlyArmsTheNamedEntries(t *testing.T) {
	_, _ = cronsTestHome(t)
	cronsFakeProver(t)
	repo := cronsTestGitRepo(t, cronsTwoSidedRepo)

	out := captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--only", "sweep/quick", "--no-timer", "-o", "json"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	report := oneJSONRecord[cronsInstallReport](t, out)
	if report.Armed != 1 || len(report.Repos[0].Schedules) != 1 {
		t.Fatalf("install record: %+v", report)
	}
	if !strings.HasSuffix(report.Repos[0].Schedules[0].Name, "/sweep/quick") {
		t.Fatalf("armed the wrong entry: %+v", report.Repos[0].Schedules[0])
	}

	var err error
	captureStdout(t, func() {
		err = runCronsInstall([]string{"--repo", repo, "--only", "nope", "--no-timer", "-o", "pretty"})
	})
	if err == nil || !strings.Contains(err.Error(), "crons install") {
		t.Fatalf("an unknown --only name did not fail the install: %v", err)
	}
}

func TestCronsInstallFollowLeavesTheScheduleOnTheCheckout(t *testing.T) {
	_, _ = cronsTestHome(t)
	cronsFakeProver(t)
	repo := cronsTestGitRepo(t, cronsMinutelyRepo)

	out := captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--follow", "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	if !strings.Contains(out, "follows the checkout") {
		t.Fatalf("install output:\n%s", out)
	}
	list := captureStdout(t, func() {
		if err := runCronsList([]string{"-o", "plain"}); err != nil {
			t.Fatalf("crons list: %v", err)
		}
	})
	if !strings.Contains(list, "\tfollows\t") {
		t.Fatalf("list plain does not carry the lock column:\n%s", list)
	}
}

func TestCronsSetAndResetRoundTripAnOverride(t *testing.T) {
	_, _ = cronsTestHome(t)
	cronsFakeProver(t)
	repo := cronsTestGitRepo(t, cronsTwoSidedRepo)
	captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})

	set := captureStdout(t, func() {
		if err := runCronsSet([]string{
			"sweep/quick", "--cron", "0 6 * * *", "--tz", "America/Denver",
			"--arg", "depth=deep", "-o", "pretty",
		}); err != nil {
			t.Fatalf("crons set: %v", err)
		}
	})
	for _, want := range []string{"0 6 * * *", "America/Denver", "cron, tz, args"} {
		if !strings.Contains(set, want) {
			t.Errorf("set output is missing %q:\n%s", want, set)
		}
	}

	show := captureStdout(t, func() {
		if err := runCronsShow([]string{"sweep/quick", "-o", "pretty"}); err != nil {
			t.Fatalf("crons show: %v", err)
		}
	})
	for _, want := range []string{"DECLARED", "OVERRIDE", "EFFECTIVE", "*/15 * * * *", "0 6 * * *", "--depth=deep"} {
		if !strings.Contains(show, want) {
			t.Errorf("show output is missing %q:\n%s", want, show)
		}
	}

	list := captureStdout(t, func() {
		if err := runCronsList([]string{"-o", "pretty"}); err != nil {
			t.Fatalf("crons list: %v", err)
		}
	})
	if !strings.Contains(list, "0 6 * * **") {
		t.Errorf("list does not mark the overridden expression:\n%s", list)
	}

	reset := captureStdout(t, func() {
		if err := runCronsReset([]string{"sweep/quick", "-o", "pretty"}); err != nil {
			t.Fatalf("crons reset: %v", err)
		}
	})
	if !strings.Contains(reset, "no override") || !strings.Contains(reset, "*/15 * * * *") {
		t.Fatalf("reset output:\n%s", reset)
	}
}

func TestCronsShowMarksAnOverrideTheRepoHasMovedUnder(t *testing.T) {
	_, _ = cronsTestHome(t)
	cronsFakeProver(t)
	repo := cronsTestGitRepo(t, cronsMinutelyRepo)
	captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--follow", "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	captureStdout(t, func() {
		if err := runCronsSet([]string{"every-minute", "--cron", "0 6 * * *", "-o", "pretty"}); err != nil {
			t.Fatalf("crons set: %v", err)
		}
	})
	writeRepoFile(t, filepath.Join(repo, ".sparkwing", "sparkwing.yaml"),
		strings.Replace(cronsMinutelyRepo, `"* * * * *"`, `"0 2 * * *"`, 1))
	captureStdout(t, func() {
		if err := runCronsTick([]string{"--dry-run", "-o", "pretty"}); err != nil {
			t.Fatalf("crons tick: %v", err)
		}
	})
	// safety: a dry-run tick does not refresh, so the declaration is republished
	// by the arm below; the stale reading comes from the refresh a real tick does.
	captureStdout(t, func() {
		if err := runCronsTick([]string{"-o", "pretty"}); err != nil {
			t.Fatalf("crons tick: %v", err)
		}
	})

	show := captureStdout(t, func() {
		if err := runCronsShow([]string{"every-minute", "-o", "pretty"}); err != nil {
			t.Fatalf("crons show: %v", err)
		}
	})
	if !strings.Contains(show, "override stale") || !strings.Contains(show, "yes:") {
		t.Fatalf("show does not mark the override stale:\n%s", show)
	}

	captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--follow", "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})
	fresh := captureStdout(t, func() {
		if err := runCronsShow([]string{"every-minute", "-o", "pretty"}); err != nil {
			t.Fatalf("crons show: %v", err)
		}
	})
	if !strings.Contains(fresh, "override stale  no") {
		t.Fatalf("re-arming did not re-base the override:\n%s", fresh)
	}
}

func TestCronsLockUnlockAndDisarmEmitOneRecordPerFormat(t *testing.T) {
	_, _ = cronsTestHome(t)
	cronsFakeProver(t)
	repo := cronsTestGitRepo(t, cronsTwoSidedRepo)
	captureStdout(t, func() {
		if err := runCronsInstall([]string{"--repo", repo, "--follow", "--no-timer", "-o", "pretty"}); err != nil {
			t.Fatalf("crons install: %v", err)
		}
	})

	locked := captureStdout(t, func() {
		if err := runCronsLock([]string{"sweep/quick", "-o", "json"}); err != nil {
			t.Fatalf("crons lock: %v", err)
		}
	})
	row := oneJSONRecord[crons.Row](t, locked)
	if row.Lock.State != crons.LockPinned || row.Lock.Binary == "" {
		t.Fatalf("lock record: %+v", row.Lock)
	}
	pinDir := filepath.Dir(row.Lock.Binary)
	if _, err := os.Stat(pinDir); err != nil {
		t.Fatalf("stat the pinned directory: %v", err)
	}

	unlocked := captureStdout(t, func() {
		if err := runCronsUnlock([]string{"sweep/quick", "-o", "pretty"}); err != nil {
			t.Fatalf("crons unlock: %v", err)
		}
	})
	if !strings.Contains(unlocked, "unpinned") || !strings.Contains(unlocked, crons.LockFollows) {
		t.Fatalf("unlock output:\n%s", unlocked)
	}
	if _, err := os.Stat(pinDir); !os.IsNotExist(err) {
		t.Errorf("unlock left the pinned directory behind: %v", err)
	}

	set := captureStdout(t, func() {
		if err := runCronsSet([]string{"sweep/quick", "--overlap", "queue", "-o", "json"}); err != nil {
			t.Fatalf("crons set: %v", err)
		}
	})
	if row = oneJSONRecord[crons.Row](t, set); row.Effective.Overlap != "queue" {
		t.Fatalf("set record: %+v", row.Effective)
	}
	reset := captureStdout(t, func() {
		if err := runCronsReset([]string{"sweep/quick", "-o", "plain"}); err != nil {
			t.Fatalf("crons reset: %v", err)
		}
	})
	if strings.TrimSpace(reset) != row.ID {
		t.Errorf("reset plain = %q, want the schedule id alone", reset)
	}

	disarmed := captureStdout(t, func() {
		if err := runCronsDisarm([]string{"sweep/quick", "-o", "plain"}); err != nil {
			t.Fatalf("crons disarm: %v", err)
		}
	})
	if strings.TrimSpace(disarmed) != filepath.Base(repo)+"/sweep/quick" {
		t.Errorf("disarm plain = %q, want the display name alone", disarmed)
	}
	list := captureStdout(t, func() {
		if err := runCronsList([]string{"--all", "-o", "pretty"}); err != nil {
			t.Fatalf("crons list: %v", err)
		}
	})
	if strings.Contains(list, "sweep/quick") {
		t.Errorf("the disarmed schedule is still listed:\n%s", list)
	}
	if !strings.Contains(list, "nightly") {
		t.Errorf("disarming one entry took the others with it:\n%s", list)
	}
}
