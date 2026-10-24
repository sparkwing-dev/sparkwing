package client

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func dashboardPeer(t *testing.T, nc net.Conn, respond func(*wingwire.Hello, *wingwire.LocalDashboard) wingwire.Message) <-chan struct{} {
	t.Helper()
	return localCommandPeer(t, nc, func(hello *wingwire.Hello, message wingwire.Message) wingwire.Message {
		request, ok := message.(*wingwire.LocalDashboard)
		if !ok {
			t.Errorf("expected dashboard request, got %T", message)
			return &wingwire.Unsupported{Type: string(wingwire.TypeOf(message))}
		}
		return respond(hello, request)
	})
}

func TestLocalDashboardExchangeWaitsForServingOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer, server := net.Pipe()
		defer peer.Close()
		entered, ready := make(chan struct{}), make(chan struct{})
		done := dashboardPeer(t, server, func(hello *wingwire.Hello, request *wingwire.LocalDashboard) wingwire.Message {
			if hello.HealthProbe || request.Address != "127.0.0.1:0" {
				t.Errorf("dashboard request: %+v %+v", hello, request)
			}
			close(entered)
			<-ready
			return &wingwire.LocalDashboardAck{}
		})
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- exchangeLocalDashboard(ctx, peer, "fixture", "127.0.0.1:0") }()
		<-entered
		synctest.Wait()
		<-time.NewTimer(2 * probeTimeout).C
		select {
		case err := <-result:
			t.Fatalf("returned before dashboard readiness: %v", err)
		default:
		}
		close(ready)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		<-done
	})
}

func TestLocalDashboardExchangeRefusesBadReplies(t *testing.T) {
	for _, reply := range []wingwire.Message{
		&wingwire.Unsupported{Type: string(wingwire.TypeLocalDashboard)},
		&wingwire.LocalControlStatus{Running: true, PID: 1, Version: "v1"},
		&wingwire.LocalDashboardAck{Error: "dashboard address is occupied"},
	} {
		peer, server := net.Pipe()
		done := dashboardPeer(t, server, func(*wingwire.Hello, *wingwire.LocalDashboard) wingwire.Message { return reply })
		err := exchangeLocalDashboard(t.Context(), peer, "fixture", "127.0.0.1:0")
		peer.Close()
		<-done
		if err == nil {
			t.Fatalf("accepted failed dashboard reply: %+v", reply)
		}
		if failed, ok := reply.(*wingwire.LocalDashboardAck); ok && !strings.Contains(err.Error(), failed.Error) {
			t.Fatalf("lost owner failure: %v", err)
		}
	}
}

func TestLocalDashboardExchangeCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer, server := net.Pipe()
		defer peer.Close()
		defer server.Close()
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() { result <- exchangeLocalDashboard(ctx, peer, "fixture", "127.0.0.1:0") }()
		if _, err := newFrameReader(server).read(); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled exchange: %v", err)
		}
	})
}

func TestLocalDashboardExchangeCancellationAfterRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer, server := net.Pipe()
		defer peer.Close()
		entered, release := make(chan struct{}), make(chan struct{})
		done := dashboardPeer(t, server, func(*wingwire.Hello, *wingwire.LocalDashboard) wingwire.Message {
			close(entered)
			<-release
			return nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- exchangeLocalDashboard(ctx, peer, "fixture", "127.0.0.1:0") }()
		<-entered
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled dashboard readiness: %v", err)
		}
		close(release)
		<-done
	})
}

func TestLocalDashboardExchangeUnresponsiveHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer, server := net.Pipe()
		defer peer.Close()
		defer server.Close()
		if err := exchangeLocalDashboard(t.Context(), peer, "fixture", "127.0.0.1:0"); !errors.Is(err, errDaemonWedged) {
			t.Fatalf("unresponsive owner: %v", err)
		}
	})
}

func TestLocalDashboardRequiresAddressBeforeDial(t *testing.T) {
	if err := startLocalDashboard(t.Context(), t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), "address is required") {
		t.Fatalf("missing dashboard address: %v", err)
	}
}

type cancelledDashboardDeadline struct {
	net.Conn
	cancel    context.CancelFunc
	closed    chan struct{}
	once      sync.Once
	deadlines int
}

func (c *cancelledDashboardDeadline) SetDeadline(deadline time.Time) error {
	c.deadlines++
	if c.deadlines == 2 {
		c.cancel()
		<-c.closed
	}
	return c.Conn.SetDeadline(deadline)
}

func (c *cancelledDashboardDeadline) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

func TestLocalDashboardExchangeCancellationDuringDeadlineChange(t *testing.T) {
	peer, server := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	nc := &cancelledDashboardDeadline{Conn: peer, cancel: cancel, closed: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		if _, err := newFrameReader(server).read(); err != nil {
			t.Error(err)
			return
		}
		body, err := wingwire.Encode(&wingwire.HelloAck{ProtocolMajor: wingwire.ProtocolMajor})
		if err == nil {
			_, err = server.Write(body)
		}
		if err != nil {
			t.Error(err)
		}
		<-nc.closed
	}()
	err := exchangeLocalDashboard(ctx, nc, "fixture", "127.0.0.1:0")
	<-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during deadline change: %v", err)
	}
}
