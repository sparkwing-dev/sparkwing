package crons

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

const pushedRepoURL = "https://github.com/acme/widgets.git"

type pushedFixture struct {
	*Service
	Store *store.Store
}

func pushedService(t *testing.T) *pushedFixture {
	t.Helper()
	st, err := teststore.OpenExisting(t, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &pushedFixture{Service: &Service{Operator: st.AsOperator(), Schedules: teststore.ForTeam(t, st, store.DefaultTeam), ArmedBy: "operator@laptop", Side: store.CronWhereController}, Store: st}
}

func TestControllerHealthReportsTimerLoop(t *testing.T) {
	svc := pushedService(t)
	health, err := svc.ControllerHealth(context.Background(), true)
	if err != nil || health.Timer.Detail != controllerTimerDetail || !health.Timer.Enabled || !health.Timer.Installed {
		t.Fatalf("health: %+v %v", health, err)
	}
}

func TestControllerHealthReportsStoppedLoop(t *testing.T) {
	svc := pushedService(t)
	health, err := svc.ControllerHealth(context.Background(), false)
	if err != nil || health.Timer.Enabled || health.Timer.Installed || health.Detail != "the controller schedule evaluator loop is stopped" {
		t.Fatalf("health: %+v %v", health, err)
	}
}

func TestPushedRepoRecognizesCloneURL(t *testing.T) {
	if !pushedRepo(pushedRepoURL) || pushedRepo("/tmp/checkout") {
		t.Fatal("clone URL classification")
	}
}

func TestLocalHealthGivesANewTimerItsFirstMinute(t *testing.T) {
	now := time.Date(2026, 11, 14, 6, 36, 30, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		age    time.Duration
		stale  bool
		detail string
	}{
		{"just installed", 20 * time.Second, false, "just installed"},
		{"installed long ago", 10 * time.Minute, true, "not ticked within three minutes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := pushedService(t)
			svc.Side = store.CronWhereLocal
			svc.Now = func() time.Time { return now }
			home := t.TempDir()
			host := crontimer.Host{
				GOOS: "linux", Home: home, ConfigHome: filepath.Join(home, ".config"),
				SparkwingHome: filepath.Join(home, ".sparkwing"), Binary: filepath.Join(home, "sparkwing"), Version: "test",
				Exec: func(context.Context, string, ...string) (string, error) { return "", nil },
			}
			installed, err := crontimer.Install(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(installed.Path, now.Add(-tc.age), now.Add(-tc.age)); err != nil {
				t.Fatal(err)
			}
			host.Exec = func(context.Context, string, ...string) (string, error) {
				return "FragmentPath=" + installed.Path + "\nActiveState=active\nUnitFileState=enabled\n", nil
			}
			svc.NativeTimer = &host
			health, err := svc.ControllerHealth(t.Context(), false)
			if err != nil || !health.Timer.Enabled || health.TickStale != tc.stale {
				t.Fatalf("health: %+v %v", health, err)
			}
			if tc.stale {
				health.Armed = 1
			}
			if detail := health.describeController(now); !strings.Contains(detail, tc.detail) {
				t.Fatalf("detail = %q, want %q", detail, tc.detail)
			}
		})
	}
}
