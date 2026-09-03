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

func TestRequestReportsAnUnsupportedReply(t *testing.T) {
	home := shortHome(t)
	serveFakeDaemon(t, home, func(nc net.Conn, r *frameReader) {
		if _, err := r.read(); err != nil {
			return
		}
		line, err := wingwire.Encode(&wingwire.Unsupported{Type: string(wingwire.TypeStatsReset)})
		if err != nil {
			return
		}
		_, _ = nc.Write(line)
		time.Sleep(time.Second)
	})

	cl := connectToFakeDaemon(t, home)
	_, err := cl.Request(&wingwire.StatsReset{})
	if !errors.Is(err, ErrDaemonLacksOperation) {
		t.Fatalf("Request error = %v, want ErrDaemonLacksOperation", err)
	}
	for _, want := range []string{"stats_reset", fakeDaemonVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q omits %q", err, want)
		}
	}
}

func TestRequestReportsADaemonThatClosesInsteadOfAnswering(t *testing.T) {
	home := shortHome(t)
	serveFakeDaemon(t, home, func(_ net.Conn, r *frameReader) {
		_, _ = r.read()
	})

	cl := connectToFakeDaemon(t, home)
	_, err := cl.Request(&wingwire.StatsReset{})
	if !errors.Is(err, ErrDaemonLacksOperation) {
		t.Fatalf("Request error = %v, want ErrDaemonLacksOperation from a daemon that predates the unsupported reply", err)
	}
	if !strings.Contains(err.Error(), "stats_reset") {
		t.Errorf("error %q does not name the operation the daemon refused", err)
	}
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
