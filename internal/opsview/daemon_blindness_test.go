package opsview_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func renderLocalQueueFor(t *testing.T, home, format string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), strayTestWait)
	defer cancel()
	qs, err := wingdclient.Query(ctx, wingdclient.Options{
		Home:        home,
		Version:     "v1.0.0",
		DialTimeout: 500 * time.Millisecond,
		Backoff:     20 * time.Millisecond,
	})
	var buf bytes.Buffer
	switch {
	case err == nil:
		if rerr := opsview.RenderLocalQueue(&buf, qs, opsview.Serving(), format); rerr != nil {
			t.Fatalf("render serving: %v", rerr)
		}
	case errors.Is(err, wingdclient.ErrDaemonUnreachable):
		if rerr := opsview.RenderUnreachableDaemon(&buf, format, err); rerr != nil {
			t.Fatalf("render unreachable: %v", rerr)
		}
	case errors.Is(err, wingdclient.ErrNoDaemon):
		if rerr := opsview.RenderNoDaemon(&buf, format); rerr != nil {
			t.Fatalf("render absent: %v", rerr)
		}
	default:
		t.Fatalf("query %s: %v", home, err)
	}
	return buf.String()
}

func TestLocalQueue_NoDaemonDoesNotReadLikeAnIdleDaemon(t *testing.T) {
	quiet := shortHome(t)
	idle := shortHome(t)
	serveDaemon(t, idle, "v1.0.0")

	for _, format := range []string{"json", "plain", "pretty"} {
		withoutDaemon := renderLocalQueueFor(t, quiet, format)
		withIdleDaemon := renderLocalQueueFor(t, idle, format)
		if withoutDaemon == withIdleDaemon {
			t.Errorf("%s: a home with no daemon renders identically to one with an idle daemon:\n%s", format, withoutDaemon)
		}
	}

	jsonNoDaemon := renderLocalQueueFor(t, quiet, "json")
	if strings.TrimSpace(jsonNoDaemon) == "{}" {
		t.Fatalf("no-daemon json is still a bare {}, which is what an idle daemon looks like:\n%s", jsonNoDaemon)
	}
	if !strings.Contains(jsonNoDaemon, `"reachable": false`) {
		t.Errorf("no-daemon json does not state that no daemon was reached:\n%s", jsonNoDaemon)
	}
	if !strings.Contains(renderLocalQueueFor(t, idle, "json"), `"reachable": true`) {
		t.Errorf("idle-daemon json does not state that the daemon was reached")
	}
}

func TestLocalQueue_UnreachableIsNeitherIdleNorAbsent(t *testing.T) {
	blocked := errors.New("dial unix /tmp/sparkwing-502-abc/d.sock: connect: operation not permitted")
	for _, format := range []string{"json", "plain", "pretty"} {
		var unreachable, absent, idle bytes.Buffer
		if err := opsview.RenderUnreachableDaemon(&unreachable, format, blocked); err != nil {
			t.Fatalf("%s unreachable: %v", format, err)
		}
		if err := opsview.RenderNoDaemon(&absent, format); err != nil {
			t.Fatalf("%s absent: %v", format, err)
		}
		if err := opsview.RenderLocalQueue(&idle, wingwire.QueueState{}, opsview.Serving(), format); err != nil {
			t.Fatalf("%s idle: %v", format, err)
		}
		if unreachable.String() == absent.String() {
			t.Errorf("%s: unreachable renders the same as absent:\n%s", format, unreachable.String())
		}
		if unreachable.String() == idle.String() {
			t.Errorf("%s: unreachable renders the same as an idle daemon:\n%s", format, unreachable.String())
		}
	}

	var pretty bytes.Buffer
	if err := opsview.RenderUnreachableDaemon(&pretty, "pretty", blocked); err != nil {
		t.Fatalf("pretty: %v", err)
	}
	if !strings.Contains(pretty.String(), "operation not permitted") {
		t.Errorf("unreachable view buries the dial cause instead of leading with it:\n%s", pretty.String())
	}
}

func TestDoctor_UnreachedDaemonDoesNotReadLikeAHealthyOne(t *testing.T) {
	quiet := shortHome(t)
	healthy := shortHome(t)
	serveDaemon(t, healthy, "v1.0.0")

	withoutDaemon := diagnoseHome(t, quiet)
	withDaemon := diagnoseHome(t, healthy)

	if withoutDaemon.Daemon.State == withDaemon.Daemon.State {
		t.Fatalf("daemon state is %q for both a home with a daemon and one without", withDaemon.Daemon.State)
	}
	if !withDaemon.Daemon.Reachable {
		t.Errorf("a served home reported an unreachable daemon: %+v", withDaemon.Daemon)
	}
	if withDaemon.Daemon.Version != "v1.0.0" {
		t.Errorf("daemon version = %q, want v1.0.0", withDaemon.Daemon.Version)
	}

	for _, format := range []string{"json", "plain", "pretty"} {
		var blind, served bytes.Buffer
		if err := opsview.RenderDoctor(&blind, withoutDaemon, format, ""); err != nil {
			t.Fatalf("%s: render blind: %v", format, err)
		}
		if err := opsview.RenderDoctor(&served, withDaemon, format, ""); err != nil {
			t.Fatalf("%s: render served: %v", format, err)
		}
		if blind.String() == served.String() {
			t.Errorf("%s: doctor output with no daemon is identical to output with a healthy one:\n%s", format, blind.String())
		}
	}
}

func TestDiagnose_NamesAVersionSkewAgainstALiveDaemon(t *testing.T) {
	home := shortHome(t)
	serveDaemon(t, home, "v9.9.9")

	report := diagnoseHome(t, home)
	skew := report.DaemonVersionSkew
	if skew == nil {
		t.Fatalf("a v9.9.9 daemon under a v1.0.0 binary produced no skew finding: %+v", report)
	}
	if skew.Self != "v1.0.0" || skew.Daemon != "v9.9.9" {
		t.Errorf("skew = %+v, want self v1.0.0 against daemon v9.9.9", skew)
	}
	if report.Clean() {
		t.Error("a report naming a version skew read as clean")
	}
}

func TestDoctor_BlindSweepIsNotClean(t *testing.T) {
	blind := opsview.DoctorReport{Daemon: opsview.DoctorDaemon{
		State:  opsview.ReachUnreachable,
		Socket: "/tmp/sparkwing-502-abc/d.sock",
		Detail: "dial unix /tmp/sparkwing-502-abc/d.sock: connect: operation not permitted",
	}}
	if blind.Clean() {
		t.Fatal("a sweep that never reached the daemon reported a clean bill")
	}

	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, blind, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "healthy") {
		t.Errorf("blind sweep still printed the healthy line:\n%s", out)
	}
	for _, want := range []string{"could not reach the admission daemon", "operation not permitted", "/tmp/sparkwing-502-abc/d.sock"} {
		if !strings.Contains(out, want) {
			t.Errorf("blind sweep output does not carry %q:\n%s", want, out)
		}
	}
}

func TestDoctor_AbsentDaemonIsCleanAndStated(t *testing.T) {
	absent := opsview.DoctorReport{Daemon: opsview.DoctorDaemon{
		State:  opsview.ReachAbsent,
		Socket: "/tmp/sparkwing-502-abc/d.sock",
		Detail: "nothing is listening; no admission is being arbitrated on this home",
	}}
	if !absent.Clean() {
		t.Fatal("an idle home with no daemon reported unclean")
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, absent, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "daemon: none running") {
		t.Errorf("healthy path said nothing about the daemon:\n%s", buf.String())
	}
}

func TestRenderDoctorJSON_AlwaysCarriesTheDaemonSection(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, opsview.DoctorReport{}, "json", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), `"daemon"`) || !strings.Contains(buf.String(), `"reachable"`) {
		t.Errorf("json dropped the daemon section:\n%s", buf.String())
	}
}

func blockSocket(t *testing.T, home string) {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatalf("place socket file: %v", err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

func TestDiagnose_LeavesRunRowsAloneWhenBlind(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reaches a socket whatever its directory mode")
	}
	home := shortHome(t)
	p := paths.PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-held", Pipeline: "demo", Status: "running",
		StartedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`,
		time.Now().Add(-10*time.Minute).UnixNano(), "run-held"); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}
	_ = st.Close()

	blockSocket(t, home)
	sweep, cancel := context.WithTimeout(ctx, strayTestWait)
	defer cancel()
	report, err := opsview.Diagnose(sweep, p, home, "v1.0.0", false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if report.Daemon.State != opsview.ReachUnreachable {
		t.Fatalf("daemon state = %q, want unreachable (the socket directory was made unsearchable)", report.Daemon.State)
	}
	if report.Clean() {
		t.Error("a sweep blinded by an unreachable socket reported a clean bill")
	}
	if len(report.OrphanedRuns) != 0 {
		t.Errorf("blind sweep finalized %v; the daemon may be holding those leases", report.OrphanedRuns)
	}

	st, err = store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = st.Close() }()
	held, err := st.GetRun(ctx, "run-held")
	if err != nil || held == nil || held.Status != "running" {
		t.Fatalf("run-held = %+v (err %v), want still running", held, err)
	}
}
