//go:build !windows

package wingd

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type apiRecorder struct {
	mu    sync.Mutex
	uids  []int
	held  chan net.Conn
	serve chan struct{}
}

func newAPIRecorder() *apiRecorder {
	return &apiRecorder{held: make(chan net.Conn, 8), serve: make(chan struct{})}
}

func (a *apiRecorder) accept(ctx context.Context, ln net.Listener) {
	close(a.serve)
	for {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		if peer, ok := nc.(APIConn); ok {
			a.mu.Lock()
			a.uids = append(a.uids, peer.PeerUID())
			a.mu.Unlock()
		}
		a.held <- nc
	}
}

func (a *apiRecorder) peers() []int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]int(nil), a.uids...)
}

func startAPIDaemon(t *testing.T, cfg Config) (*Daemon, *apiRecorder, <-chan error) {
	t.Helper()
	rec := newAPIRecorder()
	if cfg.Home == "" {
		home, err := os.MkdirTemp("/tmp", "wdapi")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(home) })
		cfg.Home = home
	}
	if cfg.Version == "" {
		cfg.Version = "v1.0.0"
	}
	cfg.ServeAPI = rec.accept
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		errc <- d.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop after cancel")
		}
	})
	select {
	case <-d.Ready():
	case err := <-errc:
		cancel()
		t.Fatalf("daemon exited before serving: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("daemon never became ready")
	}
	return d, rec, errc
}

func TestAPISocketSitsBesideAdmissionAtMode0600(t *testing.T) {
	d, rec, _ := startAPIDaemon(t, Config{})
	select {
	case <-rec.serve:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon never handed the API listener over")
	}
	sock := d.layout.apiSock
	if dir := d.layout.sock; sock != dir[:strings.LastIndex(dir, "/")]+"/api.sock" {
		t.Fatalf("api socket %s does not sit beside %s", sock, dir)
	}
	info, err := os.Lstat(sock)
	if err != nil {
		t.Fatalf("stat api socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is %s, want a socket", sock, info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("api socket mode %#o, want 0600", perm)
	}

	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial api socket: %v", err)
	}
	defer func() { _ = nc.Close() }()
	select {
	case accepted := <-rec.held:
		defer func() { _ = accepted.Close() }()
	case <-time.After(3 * time.Second):
		t.Fatal("the API server never saw the connection")
	}
	peers := rec.peers()
	if len(peers) != 1 || peers[0] != os.Getuid() {
		t.Fatalf("peer uids %v, want [%d]", peers, os.Getuid())
	}
}

func TestAPISocketRefusesAPeerFromAnotherAccount(t *testing.T) {
	restore := readPeerUID
	foreign := os.Getuid() + 1
	readPeerUID = func(net.Conn) (int, bool, error) { return foreign, true, nil }
	defer func() { readPeerUID = restore }()

	var mu sync.Mutex
	var logs strings.Builder
	d, rec, _ := startAPIDaemon(t, Config{Logf: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(&logs, format+"\n", args...)
	}})
	select {
	case <-rec.serve:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon never handed the API listener over")
	}

	nc, err := net.Dial("unix", d.layout.apiSock)
	if err != nil {
		t.Fatalf("dial api socket: %v", err)
	}
	defer func() { _ = nc.Close() }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		got := logs.String()
		mu.Unlock()
		if strings.Contains(got, fmt.Sprintf("refused a connection from uid %d", foreign)) {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the daemon log does not record the refusal:\n%s", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-rec.held:
		t.Fatal("the API server was handed a connection from another account")
	case <-time.After(200 * time.Millisecond):
	}
	if peers := rec.peers(); len(peers) != 0 {
		t.Fatalf("peer uids %v, want none", peers)
	}
}

func TestAPIListenerClosesBeforeTheDrainIsAcknowledged(t *testing.T) {
	d, rec, _ := startAPIDaemon(t, Config{})
	select {
	case <-rec.serve:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon never handed the API listener over")
	}
	sock := d.layout.apiSock
	if nc, err := net.Dial("unix", sock); err != nil {
		t.Fatalf("api socket refused a connection before the drain: %v", err)
	} else {
		_ = nc.Close()
	}

	nc, err := net.Dial("unix", d.layout.sock)
	if err != nil {
		t.Fatalf("dial admission socket: %v", err)
	}
	defer func() { _ = nc.Close() }()
	_ = nc.SetDeadline(time.Now().Add(5 * time.Second))
	send(t, nc, &wingwire.Hello{ProtocolMajor: ProtocolMajor, BinaryVersion: "test"})
	lines := bufio.NewReader(nc)
	ack := receive(t, lines)
	hello, ok := ack.(*wingwire.HelloAck)
	if !ok {
		t.Fatalf("handshake answered with %T, want a HelloAck", ack)
	}
	if hello.APIReady == nil || !*hello.APIReady {
		t.Fatalf("HelloAck reports api_ready %v, want true while the socket serves", hello.APIReady)
	}
	send(t, nc, &wingwire.DrainRequest{SuccessorVersion: "v2.0.0"})
	if msg := receive(t, lines); !isDrainAck(msg) {
		t.Fatalf("drain answered with %T, want a DrainAck", msg)
	}
	if conn, derr := net.Dial("unix", sock); derr == nil {
		_ = conn.Close()
		t.Fatal("the API socket still accepted a connection after the drain was acknowledged")
	}
}

func isDrainAck(msg wingwire.Message) bool {
	_, ok := msg.(*wingwire.DrainAck)
	return ok
}

func receive(t *testing.T, r *bufio.Reader) wingwire.Message {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	msg, err := wingwire.Decode(line)
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return msg
}

func send(t *testing.T, nc net.Conn, msg wingwire.Message) {
	t.Helper()
	line, err := wingwire.Encode(msg)
	if err != nil {
		t.Fatalf("encode %T: %v", msg, err)
	}
	if _, err := nc.Write(line); err != nil {
		t.Fatalf("write %T: %v", msg, err)
	}
}

func TestAnOpenAPIConnectionKeepsTheDaemonFromIdling(t *testing.T) {
	d, rec, errc := startAPIDaemon(t, Config{
		IdleTimeout:      300 * time.Millisecond,
		GraceWindow:      -1,
		HeadroomFraction: -1,
	})
	select {
	case <-rec.serve:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon never handed the API listener over")
	}
	nc, err := net.Dial("unix", d.layout.apiSock)
	if err != nil {
		t.Fatalf("dial api socket: %v", err)
	}
	defer func() { _ = nc.Close() }()
	var accepted net.Conn
	select {
	case accepted = <-rec.held:
	case <-time.After(3 * time.Second):
		t.Fatal("the API server never saw the connection")
	}

	select {
	case err := <-errc:
		t.Fatalf("the daemon idled out while an API connection was open: %v", err)
	case <-time.After(1500 * time.Millisecond):
	}

	// safety: an HTTP server closes its own end when the peer goes away, and
	// that close is what the idle predicate counts, so the test releases the
	// server side rather than only the dialing one.
	_ = accepted.Close()
	select {
	case <-errc:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not idle out after the API connection closed")
	}
}

func TestReapingADeadSocketDirectoryRemovesBothSockets(t *testing.T) {
	deadHome := t.TempDir()
	sock, err := SocketPath(deadHome)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("make socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	apiSock, err := APISocketPath(deadHome)
	if err != nil {
		t.Fatalf("api socket path: %v", err)
	}
	for _, path := range []string{sock, apiSock} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	if _, err := PeerSockets(t.TempDir()); err != nil {
		t.Fatalf("peer sockets: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a killed daemon's socket directory survives the sweep because api.sock is still in it: %v", err)
	}
}
