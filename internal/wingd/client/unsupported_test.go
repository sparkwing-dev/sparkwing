package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const fakeDaemonVersion = "v1.0.0"

func serveFakeDaemon(t *testing.T, home string, afterHandshake func(nc net.Conn, r *frameReader)) {
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
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		r := newFrameReader(nc)
		if _, err := r.read(); err != nil {
			return
		}
		line, err := wingwire.Encode(&wingwire.HelloAck{
			ProtocolMajor:       wingd.ProtocolMajor,
			NativeProtocolMajor: wingd.ProtocolMajor,
			BinaryVersion:       fakeDaemonVersion,
			BuildIdentity:       wingwire.BuildIdentity,
		})
		if err != nil {
			return
		}
		if _, err := nc.Write(line); err != nil {
			return
		}
		afterHandshake(nc, r)
	}()
}

func connectToFakeDaemon(t *testing.T, home string) *Client {
	t.Helper()
	cl, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     fakeDaemonVersion,
		NoTakeover:  true,
		Spawn:       func(string, string) error { return ErrNoDaemon },
		DialTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("connect to fake daemon: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

func TestResetStatsReportsAnUnsupportedReplyWithoutRetrying(t *testing.T) {
	home := shortHome(t)
	frames := make(chan int, 8)
	serveFakeDaemon(t, home, func(nc net.Conn, r *frameReader) {
		for {
			if _, err := r.read(); err != nil {
				return
			}
			frames <- 1
			line, err := wingwire.Encode(&wingwire.Unsupported{Type: string(wingwire.TypeStatsReset)})
			if err != nil {
				return
			}
			if _, err := nc.Write(line); err != nil {
				return
			}
		}
	})

	cl := connectToFakeDaemon(t, home)
	err := cl.ResetStats(context.Background())
	if !errors.Is(err, ErrDaemonLacksOperation) {
		t.Fatalf("ResetStats error = %v, want ErrDaemonLacksOperation", err)
	}
	if len(frames) != 1 {
		t.Errorf("client sent %d frames; a refusal is terminal, not transient", len(frames))
	}
}

func TestStopReportsADaemonThatRefusesTheDrain(t *testing.T) {
	home := shortHome(t)
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	serveFakeDaemon(t, home, func(nc net.Conn, r *frameReader) {
		if _, err := r.read(); err != nil {
			return
		}
		line, err := wingwire.Encode(&wingwire.Unsupported{Type: string(wingwire.TypeDrainRequest)})
		if err != nil {
			return
		}
		_, _ = nc.Write(line)
		// safety: the refusal only proves Stop gave up if the connection is still
		// open when Stop returns, and holding it to the end of the test rather than
		// for a fixed span leaves no goroutine behind.
		<-held
	})

	err := Stop(context.Background(), Options{Home: home, Version: fakeDaemonVersion, DialTimeout: 500 * time.Millisecond})
	if !errors.Is(err, ErrDaemonLacksOperation) {
		t.Fatalf("Stop error = %v, want ErrDaemonLacksOperation instead of waiting for a daemon that never drains", err)
	}
}

func TestSetPriorityReportsAnOlderDaemonAndHowToReplaceIt(t *testing.T) {
	home := shortHome(t)
	frames := make(chan int, 8)
	serveFakeDaemon(t, home, func(nc net.Conn, r *frameReader) {
		for {
			if _, err := r.read(); err != nil {
				return
			}
			frames <- 1
			line, err := wingwire.Encode(&wingwire.Unsupported{Type: string(wingwire.TypeSetPriority)})
			if err != nil {
				return
			}
			if _, err := nc.Write(line); err != nil {
				return
			}
		}
	})

	cl := connectToFakeDaemon(t, home)
	_, err := cl.SetPriority(context.Background(), "run-1", 3, "")
	if !errors.Is(err, ErrDaemonLacksOperation) {
		t.Fatalf("SetPriority error = %v, want ErrDaemonLacksOperation", err)
	}
	if !strings.Contains(err.Error(), fakeDaemonVersion) {
		t.Errorf("error does not name the daemon version: %v", err)
	}
	if !strings.Contains(err.Error(), "sparkwing daemon restart") {
		t.Errorf("error does not say how to replace the daemon: %v", err)
	}
	if len(frames) != 1 {
		t.Errorf("client sent %d frames; a refusal is terminal, not transient", len(frames))
	}
}
