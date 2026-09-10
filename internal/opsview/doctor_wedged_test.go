package opsview_test

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// serveWedgedSocket binds home's admission socket to a listener that accepts
// connections and answers only what greet returns, which is the daemon fault
// doctor exists to report and the one it cannot ask the daemon about.
func serveWedgedSocket(t *testing.T, home string, greet func(*bufio.Reader, net.Conn)) string {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("make socket dir: %v", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen on %s: %v", sock, err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		var conns []net.Conn
		defer func() {
			for _, c := range conns {
				_ = c.Close()
			}
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c)
			if greet != nil {
				greet(bufio.NewReader(c), c)
			}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-served
		_ = os.RemoveAll(filepath.Dir(sock))
	})
	return sock
}

// answerHandshakeOnly replies to the probe's hello and then says nothing,
// which is the daemon that passed its handshake and wedged behind it.
func answerHandshakeOnly(r *bufio.Reader, c net.Conn) {
	if _, err := r.ReadBytes('\n'); err != nil {
		return
	}
	ack, err := wingwire.Encode(&wingwire.HelloAck{
		ProtocolMajor: wingd.ProtocolMajor,
		BinaryVersion: "v1.0.0",
	})
	if err != nil {
		return
	}
	_, _ = c.Write(ack)
}

func seedRunningRun(t *testing.T, p paths.Paths, id string) {
	t.Helper()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(context.Background(), store.Run{
		ID: id, Pipeline: "demo", Status: "running",
		StartedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
}

func diagnoseWithBudget(t *testing.T, p paths.Paths, home string, budget time.Duration) opsview.DoctorReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	report, err := opsview.Diagnose(ctx, p, home, "v1.0.0", true)
	if err != nil {
		t.Fatalf("a daemon that answers nothing spent doctor's whole budget instead of degrading the report: %v", err)
	}
	return report
}

func wedgedHome(t *testing.T, greet func(*bufio.Reader, net.Conn)) (paths.Paths, string, string) {
	t.Helper()
	home := shortHome(t)
	p := paths.PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	seedRunningRun(t, p, "run-held")
	return p, home, serveWedgedSocket(t, home, greet)
}

func TestDiagnose_NamesADaemonThatAnswersNothing(t *testing.T) {
	p, home, sock := wedgedHome(t, nil)

	report := diagnoseWithBudget(t, p, home, strayTestWait)
	if !report.Daemon.Wedged {
		t.Fatalf("doctor did not name the wedge: %+v", report.Daemon)
	}
	if report.Daemon.State != opsview.ReachUnreachable {
		t.Errorf("daemon state = %q, want %q", report.Daemon.State, opsview.ReachUnreachable)
	}
	if report.Daemon.Socket != sock {
		t.Errorf("daemon socket = %q, want %q", report.Daemon.Socket, sock)
	}
	if report.Clean() {
		t.Error("a wedged daemon read as a clean machine")
	}
	if len(report.OrphanedRuns) != 0 {
		t.Errorf("a blind sweep finalized %v; the wedged daemon may still hold those leases", report.OrphanedRuns)
	}
}

func TestDiagnose_NamesADaemonThatWedgesBehindItsHandshake(t *testing.T) {
	p, home, _ := wedgedHome(t, answerHandshakeOnly)

	report := diagnoseWithBudget(t, p, home, strayTestWait)
	if !report.Daemon.Wedged {
		t.Fatalf("doctor did not name a daemon that wedged after its handshake: %+v", report.Daemon)
	}
	if !strings.Contains(report.Daemon.Detail, "handshake") {
		t.Errorf("the detail does not say the wedge came after a handshake: %q", report.Daemon.Detail)
	}
	if report.Clean() {
		t.Error("a daemon that answers no queue state read as a clean machine")
	}
}

// A budget smaller than the probe's own ceiling teaches doctor nothing about
// the daemon, so it must still return the rest of the report rather than the
// bare deadline error, and must not accuse the daemon of a wedge it did not
// prove.
func TestDiagnose_DegradesRatherThanSpendingAShortBudgetOnTheDaemon(t *testing.T) {
	p, home, _ := wedgedHome(t, nil)

	report := diagnoseWithBudget(t, p, home, time.Second)
	if report.Daemon.State != opsview.ReachUnreachable {
		t.Errorf("daemon state = %q, want %q", report.Daemon.State, opsview.ReachUnreachable)
	}
	if report.Daemon.Wedged {
		t.Error("doctor claimed a wedge on a budget shorter than its own probe ceiling")
	}
	if report.Clean() {
		t.Error("an unreached daemon read as a clean machine")
	}
}

func TestRenderDoctor_NamesAWedgedDaemonAndItsRecovery(t *testing.T) {
	r := opsview.DoctorReport{Daemon: opsview.DoctorDaemon{
		State:  opsview.ReachUnreachable,
		Wedged: true,
		Socket: "/tmp/sparkwing-0-abc123def456/d.sock",
		Detail: "the daemon holds this socket and answered nothing",
	}}
	var pretty bytes.Buffer
	if err := opsview.RenderDoctor(&pretty, r, "", ""); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := pretty.String()
	if !strings.Contains(out, "wedged") {
		t.Errorf("pretty report does not call the daemon wedged:\n%s", out)
	}
	if !strings.Contains(out, "/tmp/sparkwing-0-abc123def456/d.sock") {
		t.Errorf("pretty report does not name the wedged socket:\n%s", out)
	}
	if !strings.Contains(out, "lsof") || !strings.Contains(out, "USR1") {
		t.Errorf("pretty report gives no recovery commands for a wedge:\n%s", out)
	}

	var plain bytes.Buffer
	if err := opsview.RenderDoctor(&plain, r, "plain", ""); err != nil {
		t.Fatalf("render plain: %v", err)
	}
	if !strings.Contains(plain.String(), "daemon_wedged\t1") {
		t.Errorf("plain report cannot tell a held socket from a dead one:\n%s", plain.String())
	}
}
