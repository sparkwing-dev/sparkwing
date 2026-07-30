package wingd_test

import (
	"net"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// dialAtMajor opens a raw connection to home's daemon and completes the
// handshake as a client speaking major, which the real client library
// cannot do for a major other than its own.
func dialAtMajor(t *testing.T, home string, major int) (net.Conn, wingwire.HelloAck) {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	if err := writeRawMessage(nc, &wingwire.Hello{ProtocolMajor: major, BinaryVersion: "test"}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	msg := readRawMessage(t, nc)
	ack, ok := msg.(*wingwire.HelloAck)
	if !ok {
		t.Fatalf("hello response = %T, want hello_ack", msg)
	}
	return nc, *ack
}

func TestHandshakeServesOlderClientOnItsOwnProtocolMajor(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	_, ack := dialAtMajor(t, home, wingd.MinProtocolMajor)
	if ack.ProtocolMajor != wingd.MinProtocolMajor {
		t.Errorf("served major = %d, want the client's %d", ack.ProtocolMajor, wingd.MinProtocolMajor)
	}
	if ack.NativeProtocolMajor != wingd.ProtocolMajor {
		t.Errorf("native major = %d, want the daemon's %d", ack.NativeProtocolMajor, wingd.ProtocolMajor)
	}
}

func TestHandshakeAnswersNewerClientWithItsOwnMajor(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	_, ack := dialAtMajor(t, home, wingd.ProtocolMajor+1)
	if ack.ProtocolMajor != wingd.ProtocolMajor {
		t.Errorf("served major = %d, want the daemon's %d so the client takes over", ack.ProtocolMajor, wingd.ProtocolMajor)
	}
}

func TestHandshakeAnswersBelowFloorClientWithItsOwnMajor(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	_, ack := dialAtMajor(t, home, wingd.MinProtocolMajor-1)
	if ack.ProtocolMajor != wingd.ProtocolMajor {
		t.Errorf("served major = %d, want the daemon's %d so the client refuses", ack.ProtocolMajor, wingd.ProtocolMajor)
	}
}

func TestOlderProtocolClientIsGrantedAdmission(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	nc, ack := dialAtMajor(t, home, wingd.MinProtocolMajor)
	if ack.ProtocolMajor != wingd.MinProtocolMajor {
		t.Fatalf("served major = %d, want %d", ack.ProtocolMajor, wingd.MinProtocolMajor)
	}
	req := wingwire.AdmissionRequest{
		RunID:     "run-old-pin",
		Resources: wingwire.HostResources{Cores: 1},
	}
	if err := writeRawMessage(nc, &req); err != nil {
		t.Fatalf("write admission request: %v", err)
	}
	msg := readRawMessage(t, nc)
	grant, ok := msg.(*wingwire.Grant)
	if !ok {
		t.Fatalf("admission response = %T, want grant", msg)
	}
	if grant.RunID != req.RunID {
		t.Errorf("granted run %q, want %q", grant.RunID, req.RunID)
	}
	if grant.LeaseToken == "" {
		t.Error("grant carries no lease token")
	}
}

// A semaphores-only request means an internal, non-finalizing acquisition
// in the oldest served major, and a run-level claim that does finalize in
// this build's. Reading the older client's request by this build's rule
// would finalize a row its run also writes for itself.
func TestOlderProtocolSemaphoresOnlyLeaseDoesNotFinalizeRun(t *testing.T) {
	home := shortHome(t)
	finalized := make(chan string, 1)
	startDaemon(t, wingd.Config{
		Home:        home,
		FinalizeRun: func(runID string) { finalized <- runID },
	})

	nc, ack := dialAtMajor(t, home, wingd.MinProtocolMajor)
	if ack.ProtocolMajor != wingd.MinProtocolMajor {
		t.Fatalf("served major = %d, want %d", ack.ProtocolMajor, wingd.MinProtocolMajor)
	}
	req := wingwire.AdmissionRequest{
		RunID:          "run-old-pin/node",
		SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{
			Name: "deploy", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue,
		}},
	}
	if err := writeRawMessage(nc, &req); err != nil {
		t.Fatalf("write admission request: %v", err)
	}
	if msg := readRawMessage(t, nc); !isGrant(msg) {
		t.Fatalf("admission response = %T, want grant", msg)
	}
	_ = nc.Close()

	select {
	case got := <-finalized:
		t.Fatalf("older-protocol semaphores-only lease finalized run %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func isGrant(msg wingwire.Message) bool {
	_, ok := msg.(*wingwire.Grant)
	return ok
}
