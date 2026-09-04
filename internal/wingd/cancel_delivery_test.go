package wingd

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type readResult struct {
	msg wingwire.Message
	err error
}

func readAsync(peer *conn) <-chan readResult {
	out := make(chan readResult, 1)
	go func() {
		msg, err := peer.readMessage()
		out <- readResult{msg: msg, err: err}
	}()
	return out
}

func waitFrame(t *testing.T, ch <-chan readResult, what string) wingwire.Message {
	t.Helper()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("read %s: %v", what, got.err)
		}
		return got.msg
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s within five seconds", what)
	}
	return nil
}

func exclusiveClaim() []wingwire.SemaphoreClaim {
	return []wingwire.SemaphoreClaim{{Name: "excl", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}}
}

func TestCancelLeaseFlushesPromotionsWhenTheCancelledRunIsUnreachable(t *testing.T) {
	d := handlerDaemon(t, 4)

	target, targetPeer := handlerConn(t, d)
	mustGrantFrame(t, callAndRead(t, targetPeer, func() {
		d.handleAdmission(target, &wingwire.AdmissionRequest{
			RunID: "target-run", SemaphoresOnly: true, Semaphores: exclusiveClaim(),
		})
	}))

	waiter, waiterPeer := handlerConn(t, d)
	queued := callAndRead(t, waiterPeer, func() {
		d.handleAdmission(waiter, &wingwire.AdmissionRequest{
			RunID: "queued-run", SemaphoresOnly: true, Semaphores: exclusiveClaim(),
		})
	})
	if _, ok := queued.(*wingwire.Queued); !ok {
		t.Fatalf("second admission = %#v, want queued", queued)
	}

	target.close()
	targetPeer.close()

	control, controlPeer := handlerConn(t, d)
	promotion := readAsync(waiterPeer)
	ack := readAsync(controlPeer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.handleCancelLease(control, &wingwire.CancelLease{RunID: "target-run"})
	}()

	grant := mustGrantFrame(t, waitFrame(t, promotion, "promotion grant"))
	if grant.RunID != "queued-run" {
		t.Fatalf("promoted run = %q, want queued-run", grant.RunID)
	}
	reply, ok := waitFrame(t, ack, "cancel acknowledgement").(*wingwire.CancelLeaseAck)
	if !ok || !reply.Found {
		t.Fatalf("cancel reply = %#v, want an acknowledgement that the run was found", reply)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel handler did not return")
	}
}
