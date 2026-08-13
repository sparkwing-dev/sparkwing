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

func TestServedDownLevelDetectsBeingTheOlderSide(t *testing.T) {
	tests := []struct {
		name string
		ack  wingwire.HelloAck
		want bool
	}{
		{
			name: "daemon serving an older client reports a higher native major",
			ack:  wingwire.HelloAck{ProtocolMajor: 1, NativeProtocolMajor: 2},
			want: true,
		},
		{
			name: "daemon on the client's own major is not down-level",
			ack:  wingwire.HelloAck{ProtocolMajor: 2, NativeProtocolMajor: 2},
			want: false,
		},
		{
			name: "daemon predating the native field is never down-level",
			ack:  wingwire.HelloAck{ProtocolMajor: 2},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := servedDownLevel(tt.ack); got != tt.want {
				t.Errorf("servedDownLevel(%+v) = %v, want %v", tt.ack, got, tt.want)
			}
		})
	}
}

// A newer daemon that serves this client down-level is the side that must
// survive, however the two binary versions happen to order.
func TestDownLevelServiceOutranksASupersedingVersion(t *testing.T) {
	ack := wingwire.HelloAck{ProtocolMajor: 1, NativeProtocolMajor: 2, BinaryVersion: "v1.0.0"}
	if !supersedes("v2.0.0", ack.BinaryVersion) {
		t.Fatal("test premise lost: a newer release no longer supersedes an older one")
	}
	if !servedDownLevel(ack) {
		t.Error("a client served below the daemon's native major must not take it over")
	}
}

// At a matching protocol major nothing outranks the version comparison, so
// a dev build's refusal to supersede a release is the only thing keeping it
// from taking the daemon over.
func TestMatchingProtocolLeavesTheTakeoverToTheVersionComparison(t *testing.T) {
	ack := wingwire.HelloAck{ProtocolMajor: 2, NativeProtocolMajor: 2, BinaryVersion: "v1.0.0"}
	if servedDownLevel(ack) {
		t.Fatal("test premise lost: a daemon on the client's own major is not down-level")
	}
	if supersedes("(devel)", ack.BinaryVersion) {
		t.Error("a dev build must not take over a release daemon at matching protocol")
	}
}

func TestMatchingProtocolRejectsAnUnorderedCrossBuildHandshake(t *testing.T) {
	ack := wingwire.HelloAck{ProtocolMajor: wingd.ProtocolMajor, NativeProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "v1.0.0"}
	err := buildMismatch("(devel)", ack)
	if !errors.Is(err, ErrBuildMismatch) {
		t.Fatalf("buildMismatch error = %v, want ErrBuildMismatch", err)
	}
	for _, want := range []string{"(devel)", "v1.0.0", "same protocol major"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("build mismatch error %q omits %q", err, want)
		}
	}
}

func TestMatchingProtocolAcceptsTheExactSameBuild(t *testing.T) {
	ack := wingwire.HelloAck{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "v1.0.0"}
	if err := buildMismatch("v1.0.0", ack); err != nil {
		t.Fatalf("exact build rejected: %v", err)
	}
}

// A daemon prerelease build version is not a usable module pin, so the
// refusal must ask for a release by protocol number rather than the dev stamp.
func TestProtocolTooOldNamesBothMajorsAndNoPinTarget(t *testing.T) {
	ack := wingwire.HelloAck{
		ProtocolMajor:       wingd.ProtocolMajor + 1,
		NativeProtocolMajor: wingd.ProtocolMajor + 1,
		BinaryVersion:       "v0.22.0-dev+b9ade496",
	}
	err := protocolTooOld("", ack)
	if !errors.Is(err, ErrProtocolTooOld) {
		t.Fatalf("error %v does not match the sentinel", err)
	}
	msg := err.Error()
	for _, want := range []string{"v0.22.0-dev+b9ade496", "protocol"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q omits %q", msg, want)
		}
	}
	if strings.Contains(msg, "v0.22.0-dev+b9ade496 or newer") {
		t.Errorf("message %q pins to an unusable dev stamp", msg)
	}
}

// A client whose binary supersedes the daemon's version must not take the
// daemon over when the daemon is already on a newer protocol and is serving
// this client down-level. Without the servedDownLevel guard the two processes
// would drain and respawn each other's daemon without bound.
func TestDownLevelServicePreventsLivelock(t *testing.T) {
	home := shortHome(t)
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
		line, _ := wingwire.Encode(&wingwire.HelloAck{
			ProtocolMajor:       wingd.ProtocolMajor,
			NativeProtocolMajor: wingd.ProtocolMajor + 1,
			BinaryVersion:       "v1.0.0",
		})
		_, _ = nc.Write(line)
		_, _ = r.read()
	}()

	spawned := make(chan struct{}, 1)
	cl, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     "v2.0.0",
		Spawn:       func(string, string) error { spawned <- struct{}{}; return nil },
		DialTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	defer cl.Close()

	select {
	case <-spawned:
		t.Fatal("down-level service triggered a takeover; livelock guard not working")
	default:
	}
}
