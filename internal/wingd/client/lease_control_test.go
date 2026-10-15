package client

import (
	"errors"
	"net"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestLeaseWatchControlReturnsServingFailure(t *testing.T) {
	peer, daemon := net.Pipe()
	defer daemon.Close()
	client := &Client{}
	client.setConn(peer)
	defer client.Close()
	lease := &Lease{cl: client, RunID: "run", Token: "held"}
	done := make(chan error, 1)
	go func() { done <- lease.WatchControl(nil, nil) }()
	if err := writeTestFrame(daemon, &wingwire.Unsupported{Type: "lease-control"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrDaemonLacksOperation) {
		t.Fatalf("watch hid serving failure: %v", err)
	}
}
