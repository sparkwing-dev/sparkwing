package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func controlPeer(t *testing.T, nc net.Conn, respond func(*wingwire.Hello, *wingwire.LocalControl) wingwire.Message) <-chan struct{} {
	t.Helper()
	return localCommandPeer(t, nc, func(hello *wingwire.Hello, message wingwire.Message) wingwire.Message {
		request, ok := message.(*wingwire.LocalControl)
		if !ok {
			t.Errorf("expected local control, got %T", message)
			return &wingwire.Unsupported{Type: string(wingwire.TypeOf(message))}
		}
		return respond(hello, request)
	})
}

func localCommandPeer(t *testing.T, nc net.Conn, respond func(*wingwire.Hello, wingwire.Message) wingwire.Message) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer nc.Close()
		reader := newFrameReader(nc)
		message, err := reader.read()
		if err != nil {
			t.Error(err)
			return
		}
		hello, ok := message.(*wingwire.Hello)
		if !ok {
			t.Errorf("expected hello, got %T", message)
			return
		}
		write := func(message wingwire.Message) bool {
			body, err := wingwire.Encode(message)
			if err == nil {
				_, err = nc.Write(body)
			}
			if err != nil {
				t.Error(err)
			}
			return err == nil
		}
		if !write(&wingwire.HelloAck{ProtocolMajor: wingwire.ProtocolMajor, BinaryVersion: "v1"}) {
			return
		}
		message, err = reader.read()
		if err != nil {
			t.Error(err)
			return
		}
		if reply := respond(hello, message); reply != nil {
			write(reply)
		}
	}()
	return done
}

func TestLocalControlExchangeReportsOwner(t *testing.T) {
	for _, mode := range []string{"query", "stay", "idle"} {
		t.Run(mode, func(t *testing.T) {
			var stay *bool
			if mode != "query" {
				value := mode == "stay"
				stay = &value
			}
			peer, server := net.Pipe()
			defer peer.Close()
			idle := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
			done := controlPeer(t, server, func(hello *wingwire.Hello, request *wingwire.LocalControl) wingwire.Message {
				if hello.HealthProbe != (stay == nil) || (request.Stay == nil) != (stay == nil) {
					t.Errorf("wrong probe/request: %+v %+v", hello, request)
				}
				if stay != nil && request.Stay != nil && *stay != *request.Stay {
					t.Error("stay value changed in transit")
				}
				return &wingwire.LocalControlStatus{Running: true, Stay: stay != nil && *stay, PID: 91, Version: "v1", Runs: 1, QueuedTriggers: 2, HeldLeases: 3, DashboardConnections: 4, IdleSince: idle}
			})
			status, err := exchangeLocalControl(t.Context(), peer, "fixture", stay)
			<-done
			if err != nil {
				t.Fatal(err)
			}
			if !status.Running || status.PID != 91 || status.Version != "v1" || status.Installed || status.Stay != (stay != nil && *stay) {
				t.Fatalf("owner: %+v", status)
			}
			if status.Activity.Runs != 1 || status.Activity.QueuedTriggers != 2 || status.Activity.HeldLeases != 3 || status.Activity.DashboardConnections != 4 || status.Activity.IdleSince != idle {
				t.Fatalf("activity: %+v", status)
			}
		})
	}
}

func TestLocalControlExchangeRefusesInvalidReplies(t *testing.T) {
	for _, reply := range []wingwire.Message{
		&wingwire.Unsupported{Type: string(wingwire.TypeLocalControl)},
		&wingwire.QueueState{},
		&wingwire.LocalControlStatus{Running: true, Version: "v1"},
		&wingwire.LocalControlStatus{Running: true, PID: 1},
		&wingwire.LocalControlStatus{Running: true, PID: 1, Version: "v1", Runs: -1},
		&wingwire.LocalControlStatus{Running: false, PID: 1, Version: "v1"},
		&wingwire.LocalControlStatus{Running: true, Stay: false, PID: 1, Version: "v1"},
	} {
		peer, server := net.Pipe()
		done := controlPeer(t, server, func(*wingwire.Hello, *wingwire.LocalControl) wingwire.Message { return reply })
		stay := true
		_, err := exchangeLocalControl(t.Context(), peer, "fixture", &stay)
		peer.Close()
		<-done
		if err == nil {
			t.Fatalf("accepted invalid stay reply: %+v", reply)
		}
	}
}

func TestLocalControlExchangeCancellationAndWedgedOwner(t *testing.T) {
	for _, cancelNow := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			peer, server := net.Pipe()
			defer peer.Close()
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelNow {
				cancel()
			}
			_, err := exchangeLocalControl(ctx, peer, "fixture", nil)
			want := errDaemonWedged
			if cancelNow {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("error=%v, want %v", err, want)
			}
		})
	}
}

func TestLocalControlCancellationInterruptsHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer, server := net.Pipe()
		defer peer.Close()
		defer server.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := exchangeLocalControl(ctx, peer, "fixture", nil); done <- err }()
		if _, err := newFrameReader(server).read(); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked handshake: %v", err)
		}
	})
}
