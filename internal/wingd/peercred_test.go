//go:build !windows

package wingd

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestPeerAllowed_AdmitsOnlyThisUser(t *testing.T) {
	self := os.Getuid()
	cases := []struct {
		name  string
		uid   int
		known bool
		want  bool
	}{
		{name: "this user", uid: self, known: true, want: true},
		{name: "another user", uid: self + 1, known: true, want: false},
		{name: "root", uid: 0, known: true, want: self == 0},
		{name: "credentials unavailable", uid: 0, known: false, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := peerAllowed(tc.uid, tc.known); got != tc.want {
				t.Fatalf("peerAllowed(%d, %v) = %v, want %v", tc.uid, tc.known, got, tc.want)
			}
		})
	}
}

func TestPeerUID_ReadsTheConnectingUser(t *testing.T) {
	sock := filepath.Join(shortSocketBase(t), "d.sock")
	ln := listenAt(t, sock)

	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	uid, known, err := peerUID(server)
	if err != nil {
		t.Fatalf("peerUID: %v", err)
	}
	if !known {
		t.Skip("this platform does not report unix socket peer credentials")
	}
	if uid != os.Getuid() {
		t.Fatalf("peerUID = %d, want this process's uid %d", uid, os.Getuid())
	}
	if err := checkPeerCredentials(server); err != nil {
		t.Fatalf("checkPeerCredentials refused this user's own connection: %v", err)
	}
}

const peerCredHelperSocket = "WINGD_PEERCRED_HELPER_SOCKET"

func TestPeerCredHelperProcess(t *testing.T) {
	sock := os.Getenv(peerCredHelperSocket)
	if sock == "" {
		t.Skip("re-exec entry point for TestDaemon_DropsAForeignUser")
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("helper uid %d could not reach the socket: %v", os.Getuid(), err)
	}
	defer func() { _ = c.Close() }()
	hello, err := wingwire.Encode(&wingwire.Hello{ProtocolMajor: ProtocolMajor, BinaryVersion: "helper"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write(hello)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if n, _ := c.Read(buf); n > 0 {
		t.Fatalf("daemon answered a connection from uid %d", os.Getuid())
	}
}

func TestDaemon_DropsAForeignUser(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skipf("running as uid %d; only root can run the helper as a different user", os.Getuid())
	}
	const foreignUID = 65534

	var mu sync.Mutex
	var logs strings.Builder
	sock := startLoggingDaemon(t, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(&logs, format+"\n", args...)
	})

	// safety: loosen the file modes so only the credential check can refuse the
	// helper, not the directory or socket permissions.
	if err := os.Chmod(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sock, 0o666); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestPeerCredHelperProcess", "-test.v")
	cmd.Env = append(os.Environ(), peerCredHelperSocket+"="+sock)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: foreignUID, Gid: foreignUID}}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper as uid %d failed: %v\n%s", foreignUID, err, out)
	}

	mu.Lock()
	got := logs.String()
	mu.Unlock()
	if !strings.Contains(got, "refused a connection from uid 65534") {
		t.Fatalf("daemon log does not record the refusal:\n%s", got)
	}
}

func startLoggingDaemon(t *testing.T, logf func(format string, args ...any)) string {
	t.Helper()
	d, err := New(Config{Home: t.TempDir(), Version: "v1.0.0", Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-errc:
		cancel()
		t.Fatalf("daemon exited before serving: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("daemon never became ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-errc:
		case <-time.After(3 * time.Second):
			t.Error("daemon did not stop after cancel")
		}
	})
	return d.SocketPath()
}
