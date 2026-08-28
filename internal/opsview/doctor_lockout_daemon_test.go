package opsview

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const lockoutDaemonWait = 30 * time.Second

func lockoutHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "opsview-lockout")
	if err != nil {
		t.Skipf("no short temp dir for a daemon socket: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func serveProtocolAck(t *testing.T, home string, major int, version string) {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ack, err := wingwire.Encode(&wingwire.HelloAck{ProtocolMajor: major, BinaryVersion: version})
	if err != nil {
		t.Fatalf("encode ack: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				r := bufio.NewReader(conn)
				if _, err := r.ReadBytes('\n'); err != nil {
					return
				}
				if _, err := conn.Write(ack); err != nil {
					return
				}
				_, _ = r.ReadBytes('\n')
			}()
		}
	}()
}

func diagnoseAgainstAck(t *testing.T, home, selfVersion string) DoctorReport {
	t.Helper()
	p := paths.PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockoutDaemonWait)
	defer cancel()
	report, err := Diagnose(ctx, p, home, selfVersion, true)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	return report
}

func TestDiagnose_NamesLockedOutReposAgainstADaemonThisBuildCannotQuery(t *testing.T) {
	home := lockoutHome(t)
	serveProtocolAck(t, home, wingwire.ProtocolMajor+1, "v0.30.0")
	base := t.TempDir()
	registerRepos(t,
		repoPinned(t, base, "workwing", "v0.17.25"),
		repoPinned(t, base, "bitwing", "v0.23.0"),
	)

	report := diagnoseAgainstAck(t, home, "v0.22.0")

	if len(report.LockedOutRepos) != 2 {
		t.Fatalf("both pins sit below the daemon's release; got %+v", report.LockedOutRepos)
	}
	for _, row := range report.LockedOutRepos {
		if row.RaiseTo != "v0.30.0" {
			t.Errorf("row %s raises to %q; only the daemon's own release is known to speak its protocol",
				row.Name, row.RaiseTo)
		}
	}
	if report.Clean() {
		t.Error("a report naming locked-out repos must not be clean")
	}
}

func TestDiagnose_PointsAtTheCLIWhenItSpeaksAnOlderProtocolThanTheDaemon(t *testing.T) {
	home := lockoutHome(t)
	serveProtocolAck(t, home, wingwire.ProtocolMajor+1, "v0.30.0")
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	report := diagnoseAgainstAck(t, home, "v0.22.0")

	gap := report.DaemonProtocolGap
	if gap == nil {
		t.Fatal("a daemon speaking a protocol this build does not is a finding of its own")
	}
	if gap.Daemon != wingwire.ProtocolMajor+1 || gap.Self != wingwire.ProtocolMajor {
		t.Errorf("protocol gap = %+v, want daemon %d against self %d",
			gap, wingwire.ProtocolMajor+1, wingwire.ProtocolMajor)
	}
	out := renderPretty(t, report)
	if strings.Contains(out, "upgrading it does not help") {
		t.Errorf("the CLI is what has to move here; the report argues against it:\n%s", out)
	}
	if !strings.Contains(out, "update the sparkwing CLI") {
		t.Errorf("pretty output does not name the lever that moves:\n%s", out)
	}
}
