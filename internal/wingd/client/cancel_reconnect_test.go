package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func silentAfterReconnect(t *testing.T, home string) (queuedAgain <-chan struct{}) {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	waiting := make(chan struct{})
	stopped := make(chan struct{})
	t.Cleanup(func() { close(stopped) })
	go func() {
		for attempt := 0; ; attempt++ {
			nc, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			r := newFrameReader(nc)
			if _, rerr := r.read(); rerr != nil {
				_ = nc.Close()
				continue
			}
			ack, _ := wingwire.Encode(&wingwire.HelloAck{
				ProtocolMajor:       wingd.ProtocolMajor,
				NativeProtocolMajor: wingd.ProtocolMajor,
				BinaryVersion:       "v1.0.0",
				BuildIdentity:       wingwire.BuildIdentity,
			})
			if _, werr := nc.Write(ack); werr != nil {
				_ = nc.Close()
				continue
			}
			if _, rerr := r.read(); rerr != nil {
				_ = nc.Close()
				continue
			}
			if attempt == 0 {
				_ = nc.Close()
				continue
			}
			close(waiting)
			<-stopped
			_ = nc.Close()
			return
		}
	}()
	return waiting
}

func TestAcquire_CancelStillInterruptsTheWaitAfterAReconnect(t *testing.T) {
	home := shortHome(t)
	queuedAgain := silentAfterReconnect(t, home)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl, err := EnsureDaemon(ctx, Options{
		Home:        home,
		Version:     "v1.0.0",
		NoTakeover:  true,
		Spawn:       func(string, string) error { return ErrNoDaemon },
		DialTimeout: 500 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	defer cl.Close()

	acquired := make(chan error, 1)
	go func() {
		_, aerr := cl.Acquire(ctx, wingwire.AdmissionRequest{
			RunID:     "r1",
			Resources: wingwire.HostResources{Cores: 0.5},
		}, nil)
		acquired <- aerr
	}()

	select {
	case <-queuedAgain:
	case <-time.After(10 * time.Second):
		t.Fatal("the client never re-submitted its request after the daemon dropped the connection")
	}
	cancel()

	select {
	case err := <-acquired:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled acquire = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not interrupt the admission wait on the reconnected socket")
	}
}

func TestCancelOnDone_LeavesTheConnectionUsableAfterACancelledWait(t *testing.T) {
	for i := 0; i < 200; i++ {
		client, daemon := net.Pipe()
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			r := newFrameReader(daemon)
			for {
				if _, err := r.read(); err != nil {
					return
				}
			}
		}()
		cl := &Client{nc: client, dec: newFrameReader(client)}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cl.cancelOnDone(ctx)()

		cl.connMu.Lock()
		stillArmed := cl.waitCancelled
		cl.connMu.Unlock()
		if stillArmed {
			t.Fatalf("wait %d: cancellation stayed armed, so the next connection would be woken the moment it is installed", i)
		}
		if err := cl.write(&wingwire.QueueState{}); err != nil {
			t.Fatalf("wait %d: the connection a cancelled wait left behind is unusable: %v", i, err)
		}

		_ = client.Close()
		_ = daemon.Close()
		<-drained
	}
}
