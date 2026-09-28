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

func writeTestFrame(nc net.Conn, msg wingwire.Message) error {
	frame, err := wingwire.Encode(msg)
	if err != nil {
		return err
	}
	_, err = nc.Write(frame)
	return err
}

func delayedReconnect(cl *Client, ready <-chan struct{}) <-chan net.Conn {
	server := make(chan net.Conn, 1)
	cl.reconnectAttempt = func(context.Context) error {
		select {
		case <-ready:
			clientEnd, serverEnd := net.Pipe()
			cl.setConn(clientEnd)
			cl.closed.Store(false)
			server <- serverEnd
			return nil
		default:
			return ErrNoDaemon
		}
	}
	return server
}

func TestQueuedAcquireRecoversAfterLongReplacementGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientEnd, oldServer := net.Pipe()
		defer oldServer.Close()
		cl := &Client{}
		cl.setConn(clientEnd)
		defer cl.Close()
		ready := make(chan struct{})
		newServer := delayedReconnect(cl, ready)
		queued := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			_, err := cl.Acquire(t.Context(), wingwire.AdmissionRequest{RunID: "queued"}, func(wingwire.Queued) { close(queued) })
			result <- err
		}()
		if msg, err := newFrameReader(oldServer).read(); err != nil {
			t.Fatal(err)
		} else if req, ok := msg.(*wingwire.AdmissionRequest); !ok || req.RunID != "queued" {
			t.Fatalf("old daemon request = %#v", msg)
		}
		if err := writeTestFrame(oldServer, &wingwire.Queued{RunID: "queued"}); err != nil {
			t.Fatal(err)
		}
		<-queued
		disconnectedAt := time.Now()
		_ = oldServer.Close()
		go func() {
			time.Sleep(9 * time.Second)
			close(ready)
		}()
		successor := <-newServer
		defer successor.Close()
		if msg, err := newFrameReader(successor).read(); err != nil {
			t.Fatal(err)
		} else if req, ok := msg.(*wingwire.AdmissionRequest); !ok || req.RunID != "queued" {
			t.Fatalf("successor request = %#v", msg)
		}
		if err := writeTestFrame(successor, &wingwire.Grant{RunID: "queued", LeaseToken: "token"}); err != nil {
			t.Fatal(err)
		}
		if err := <-result; err != nil {
			t.Fatalf("queued acquire after replacement: %v", err)
		}
		if time.Since(disconnectedAt) <= 8*time.Second {
			t.Fatal("replacement gap did not exceed eight seconds")
		}
	})
}

func TestLeaseWatchRecoversAfterLongReplacementGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientEnd, oldServer := net.Pipe()
		defer oldServer.Close()
		cl := &Client{}
		cl.setConn(clientEnd)
		lease := &Lease{cl: cl, RunID: "parent", Token: "token"}
		defer lease.Release()
		ready := make(chan struct{})
		newServer := delayedReconnect(cl, ready)
		reattached := make(chan struct{})
		watchDone := make(chan error, 1)
		go func() {
			watchDone <- lease.WatchOwnership(nil, nil, func() { close(reattached) })
		}()
		disconnectedAt := time.Now()
		_ = oldServer.Close()
		go func() {
			time.Sleep(9 * time.Second)
			close(ready)
		}()
		successor := <-newServer
		defer successor.Close()
		if msg, err := newFrameReader(successor).read(); err != nil {
			t.Fatal(err)
		} else if req, ok := msg.(*wingwire.Reattach); !ok || req.RunID != "parent" || req.LeaseToken != "token" {
			t.Fatalf("successor reattach = %#v", msg)
		}
		if err := writeTestFrame(successor, &wingwire.Grant{RunID: "parent", LeaseToken: "token"}); err != nil {
			t.Fatal(err)
		}
		<-reattached
		if time.Since(disconnectedAt) <= 8*time.Second {
			t.Fatal("replacement gap did not exceed eight seconds")
		}
		if err := lease.Release(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
		if err := <-watchDone; err != nil {
			t.Fatal(err)
		}
	})
}

func TestReleaseCancelsLeaseRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientEnd, oldServer := net.Pipe()
		cl := &Client{reconnectAttempt: func(context.Context) error { return ErrNoDaemon }}
		cl.setConn(clientEnd)
		lease := &Lease{cl: cl, RunID: "parent", Token: "token"}
		watchDone := make(chan error, 1)
		go func() { watchDone <- lease.WatchOwnership(nil, nil, nil) }()
		_ = oldServer.Close()
		time.Sleep(time.Second)
		releasedAt := time.Now()
		if err := lease.Release(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
		<-watchDone
		if time.Since(releasedAt) >= time.Second {
			t.Fatal("release waited for the lease recovery ceiling")
		}
	})
}
