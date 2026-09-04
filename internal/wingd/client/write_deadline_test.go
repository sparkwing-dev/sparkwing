package client

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestWrite_BoundsAFrameWhenTheDaemonStopsReading(t *testing.T) {
	client, daemon := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = daemon.Close()
	})
	cl := &Client{nc: client, dec: newFrameReader(client), writeTimeout: 50 * time.Millisecond}

	written := make(chan error, 1)
	go func() { written <- cl.write(&wingwire.QueueState{}) }()
	select {
	case err := <-written:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("write to a stalled daemon = %v, want a deadline failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write to a stalled daemon never returned; the run would hang with no diagnostic")
	}
}

func TestWrite_ReArmsItsDeadlineForEveryFrame(t *testing.T) {
	client, daemon := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = daemon.Close()
	})
	reading := make(chan struct{})
	go func() {
		defer close(reading)
		r := newFrameReader(daemon)
		for {
			if _, err := r.read(); err != nil {
				return
			}
		}
	}()
	cl := &Client{nc: client, dec: newFrameReader(client), writeTimeout: 50 * time.Millisecond}

	if err := cl.write(&wingwire.QueueState{}); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := cl.write(&wingwire.QueueState{}); err != nil {
		t.Fatalf("second frame after the first budget elapsed: %v", err)
	}
}

func TestWriteBudget_DefaultsToTheBoundedTimeout(t *testing.T) {
	cl := &Client{}
	if got := cl.writeBudget(); got != clientWriteTimeout {
		t.Fatalf("default write budget = %s, want %s", got, clientWriteTimeout)
	}
	if clientWriteTimeout <= 0 {
		t.Fatal("an unbounded client write budget is what hangs a run on a stalled daemon")
	}
}
