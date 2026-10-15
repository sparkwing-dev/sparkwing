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
			return crons.Proof{Binary: path}, nil
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

func TestCronsInstallFollowLeavesTheScheduleOnTheCheckout(t *testing.T) {
	_, _ = cronsTestHome(t)
	cronsFakeProver(t)
	repo := cronsTestGitRepo(t, cronsMinutelyRepo)

	out := captureStdout(t, func() {
		if err := runIn(t, repo, runCronsInstall, "--unpinned", "--no-timer", "-o", "pretty"); err != nil {
			t.Fatalf("schedules install: %v", err)
		}
	})
	if !strings.Contains(out, "follows the checkout") {
		t.Fatalf("install output:\n%s", out)
	}
	session, release, err := openCrons("")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rows, err := session.svc.List(t.Context())
	if err != nil || len(rows) != 1 || rows[0].Lock.State != crons.LockFollows {
		t.Fatalf("un-pinned schedules: %+v %v", rows, err)
	}
}

func TestCronsInstallOnlyArmsTheNamedEntries(t *testing.T) {
	_, _ = cronsTestHome(t)
	cronsFakeProver(t)
	repo := cronsTestGitRepo(t, cronsTwoSidedRepo)

	out := captureStdout(t, func() {
		if err := runIn(t, repo, runCronsInstall, "--schedule", "sweep/quick", "--no-timer", "-o", "json"); err != nil {
			t.Fatalf("schedules install: %v", err)
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
		err = runIn(t, repo, runCronsInstall, "--schedule", "nope", "--no-timer", "-o", "pretty")
	})
	if err == nil || !strings.Contains(err.Error(), "schedules install") {
		t.Fatalf("an unknown --schedule name did not fail the install: %v", err)
	}
}

func TestCronsInstallPinsLocalEntriesAndReportsTheControllerOnes(t *testing.T) {
	_, _ = cronsTestHome(t)
	cronsFakeProver(t)
	repo := cronsTestGitRepo(t, cronsTwoSidedRepo)
	base := filepath.Base(repo)
	head := cronsGit(t, repo, "rev-parse", "HEAD")

	out := captureStdout(t, func() {
		if err := runIn(t, repo, runCronsInstall, "--no-timer", "-o", "pretty"); err != nil {
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

	session, release, err := openCrons("")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rows, err := session.svc.List(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatalf("local pins: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.Lock.Ref != head || row.Where != "local" {
			t.Fatalf("incorrect local pin: %+v", row)
		}
	}
}
