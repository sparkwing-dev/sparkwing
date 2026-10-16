//go:build !windows

package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	leaseInheritorEnv  = "SPARKWING_TEST_LEASE_PATH"
	leaseGrandchildEnv = "SPARKWING_TEST_LEASE_GRANDCHILD"
)

func TestLeaseInheritorProcess(t *testing.T) {
	path := os.Getenv(leaseInheritorEnv)
	if path == "" {
		t.Skip("run only as the child of TestLocalChildHoldsTheLeaseItInheritsAndPassesItNoFurther")
	}
	if os.Getenv(leaseGrandchildEnv) != "" {
		var fd3, lease syscall.Stat_t
		if syscall.Fstat(3, &fd3) == nil && syscall.Stat(path, &lease) == nil && fd3.Ino == lease.Ino && fd3.Dev == lease.Dev {
			t.Fatal("a process the run's host started inherited the worktree lease")
		}
		return
	}
	inherited := os.NewFile(3, "lease")
	if ok, err := flockTry(inherited); err != nil || !ok {
		t.Fatalf("fd 3 is not the held lease: %v, %v", ok, err)
	}
	other, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := flockTry(other); ok {
		t.Fatal("the lease was free to a new holder while the run held it")
	}
	if err := KeepInheritedLease(); err != nil {
		t.Fatal(err)
	}
	grandchild := exec.Command(os.Args[0], "-test.run=^TestLeaseInheritorProcess$")
	grandchild.Env = append(os.Environ(), leaseGrandchildEnv+"=1")
	if out, err := grandchild.CombinedOutput(); err != nil {
		t.Fatalf("grandchild: %v: %s", err, out)
	}
}

func TestLocalChildHoldsTheLeaseItInheritsAndPassesItNoFurther(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checks.lease")
	hold, held, err := tryHoldLease(dir, path)
	if err != nil || !held {
		t.Fatalf("hold: %v, %v", held, err)
	}
	defer func() { _ = ReleaseRefWorktree(hold) }()

	env := append(os.Environ(), leaseInheritorEnv+"="+path)
	if err := execLocalChild(context.Background(), os.Args[0], dir,
		[]string{"-test.run=^TestLeaseInheritorProcess$"}, env, hold); err != nil {
		t.Fatalf("the run's host did not hold the lease alone: %v", err)
	}
}

func TestDispatchLocalTriggerHandsTheLeaseToTheRunsHost(t *testing.T) {
	repoDir, cache, logger := cronPinnedFixture(t)
	record := filepath.Join(t.TempDir(), "host")
	host := filepath.Join(t.TempDir(), "sparkwing")
	script := "#!/bin/sh\n[ \"$1\" = \"" + TriggerHostVerb + "\" ] || exit 64\nif [ -e /dev/fd/3 ]; then held=yes; else held=no; fi\nprintf '%s|%s\\n' \"$held\" \"$*\" > " + record + "\n"
	if err := os.WriteFile(host, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wingdclient.HostBinEnv, host)
	pinned := filepath.Join(t.TempDir(), "pipeline")
	if err := os.WriteFile(pinned, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	lease, held, err := tryHoldLease(dir, filepath.Join(dir, "checks.lease"))
	if err != nil || !held {
		t.Fatalf("hold: %v, %v", held, err)
	}
	defer func() { _ = ReleaseRefWorktree(lease) }()

	if err := dispatchLocalTrigger(context.Background(), &store.Trigger{
		ID:         "run-leased",
		Pipeline:   "nightly",
		TriggerEnv: map[string]string{SubmitRepoDirKey: repoDir, crons.PinnedBinaryEnvKey: pinned},
	}, repoDir, cache, logger, nil, lease); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want := "yes|" + TriggerHostVerb + " --program " + pinned + " --" + TriggerHostLeaseFlag + " run-leased"
	if got := strings.TrimSpace(string(raw)); got != want {
		t.Errorf("the run's host saw %q, want %q", got, want)
	}
}
