//go:build !windows

package wingd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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
		if peer, ok := nc.(authenticatedAPIConn); ok {
			identity := peer.PeerIdentity()
			a.mu.Lock()
			if identity.UID != nil {
				a.uids = append(a.uids, int(*identity.UID))
			}
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
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		select {
		case <-finished:
		case <-ctx.Done():
			t.Error("daemon did not stop after cancel")
		}
	})
	ready, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	select {
	case <-d.Ready():
	case err := <-errc:
		cancel()
		t.Fatalf("daemon exited before serving: %v", err)
	case <-ready.Done():
		cancel()
		t.Fatal("daemon never became ready")
	}
	return d, rec, errc
}

func TestAPISocketSitsBesideAdmissionAtMode0600(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	d, rec, _ := startAPIDaemon(t, Config{})
	select {
	case <-rec.serve:
	case <-ctx.Done():
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
	case <-ctx.Done():
		t.Fatal("the API server never saw the connection")
	}
	peers := rec.peers()
	if len(peers) != 1 || peers[0] != os.Getuid() {
		t.Fatalf("peer uids %v, want [%d]", peers, os.Getuid())
	}
}

func TestAPISocketRefusesAPeerFromAnotherAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	restore := readPeerUID
	foreign := os.Getuid() + 1
	readPeerUID = func(net.Conn) (int, bool, error) { return foreign, true, nil }
	t.Cleanup(func() { readPeerUID = restore })

	refused := make(chan struct{})
	d, rec, _ := startAPIDaemon(t, Config{Logf: func(format string, args ...any) {
		if strings.Contains(fmt.Sprintf(format, args...), fmt.Sprintf("refused a connection from uid %d", foreign)) {
			close(refused)
		}
	}})
	select {
	case <-rec.serve:
	case <-ctx.Done():
		t.Fatal("the daemon never handed the API listener over")
	}

	nc, err := net.Dial("unix", d.layout.apiSock)
	if err != nil {
		t.Fatalf("dial api socket: %v", err)
	}
	defer func() { _ = nc.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		if err := nc.SetReadDeadline(deadline); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := nc.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("foreign connection was not closed: n=%d err=%v", n, err)
	}
	select {
	case <-refused:
	case <-ctx.Done():
		t.Fatal("the daemon did not record the refusal")
	}
	select {
	case <-rec.held:
		t.Fatal("the API server was handed a connection from another account")
	default:
	}
	if peers := rec.peers(); len(peers) != 0 {
		t.Fatalf("peer uids %v, want none", peers)
	}
}
