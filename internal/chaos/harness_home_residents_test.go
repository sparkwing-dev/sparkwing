package chaos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestHomeResidentsSeesADetachedProcessOwnedGroupsMiss(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("reading a process environment is unsupported on %s", runtime.GOOS)
	}
	requireProcessGroups(t)

	home := t.TempDir()
	cmd := helperCommand("hang", 0)
	cmd.Env = append(cmd.Env, "SPARKWING_HOME="+home)
	group, err := procgroup.StartSession(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = group.Terminate(ctx, 50*time.Millisecond)
	})

	h, journal := newProcessHarness(t)
	defer journal.Close()
	h.cfg.Settle = 2 * time.Second
	h.home = home
	h.actors = map[string]*actor{}
	h.daemons = map[int]*daemonProcess{}

	if owned := h.ownedProcessGroups(); len(owned) != 0 {
		t.Fatalf("owned process groups = %v, want none; the harness never started this process", owned)
	}

	violations := h.homeResidentViolations()
	if len(violations) == 0 {
		t.Fatal("a process detached into its own session held the isolated home and teardown reported nothing")
	}
	if !slices.ContainsFunc(violations, func(v string) bool {
		return strings.Contains(v, strconv.Itoa(group.ID())) && strings.Contains(v, home)
	}) {
		t.Fatalf("violations = %v, want the detached pid %d and the home %s named", violations, group.ID(), home)
	}
}

func TestHomeResidentViolationsChargesAFailedInspection(t *testing.T) {
	h, journal := newProcessHarness(t)
	defer journal.Close()
	h.cfg.Settle = 10 * time.Millisecond
	h.home = filepath.Join(t.TempDir(), "home")
	h.residentReader = func(string) ([]int, error) { return nil, errors.New("process table unavailable") }

	violations := h.homeResidentViolations()
	if len(violations) != 1 || !strings.Contains(violations[0], "no verdict") {
		t.Fatalf("violations = %v, want the failed inspection charged as a violation", violations)
	}
}

func TestHomeResidentViolationsPassesAQuietHome(t *testing.T) {
	h, journal := newProcessHarness(t)
	defer journal.Close()
	h.cfg.Settle = 10 * time.Millisecond
	h.home = t.TempDir()
	h.residentReader = func(string) ([]int, error) { return nil, nil }

	if violations := h.homeResidentViolations(); len(violations) != 0 {
		t.Fatalf("violations = %v, want none for a home nothing holds", violations)
	}
}

func TestHomeResidentViolationsWaitsOutAProcessOnItsWayOut(t *testing.T) {
	h, journal := newProcessHarness(t)
	defer journal.Close()
	h.cfg.Settle = time.Second
	h.home = t.TempDir()
	calls := 0
	h.residentReader = func(string) ([]int, error) {
		calls++
		if calls < 3 {
			return []int{4242}, nil
		}
		return nil, nil
	}

	if violations := h.homeResidentViolations(); len(violations) != 0 {
		t.Fatalf("violations = %v, want none once the process exits inside the settle window", violations)
	}
}

func TestHomeResidentsIgnoresAnotherHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("reading a process environment is unsupported on %s", runtime.GOOS)
	}
	pids, err := homeResidents(filepath.Join(os.TempDir(), "chaos-home-no-process-holds"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 0 {
		t.Fatalf("residents = %v, want none for a home no process was started against", pids)
	}
}
