package wingd_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestSetPriority_NodeParticipantRecognizesOwningRun(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30), GraceWindow: -1})
	hog := ensure(t, home, "")
	lease := mustAcquire(t, hog, coreReq("hog", 4))
	first := ensure(t, home, "")
	firstID := string(wingwire.NodeHostParticipant("first-run", "first"))
	positions, _ := acquireAsync(first, coreReq(firstID, 1))
	waitForQueue(t, positions)
	second := ensure(t, home, "")
	secondID := string(wingwire.NodeHostParticipant("second-run", "second"))
	positions, result := acquireAsync(second, coreReq(secondID, 1))
	waitForQueue(t, positions)
	for i := 0; i < 2; i++ {
		ack := setPriority(t, home, "second-run", 0, "front")
		if !ack.Found || ack.Priority != 1 || ack.Position != 1 || ack.Participants != 1 || ack.Holding {
			t.Fatalf("front ack = %+v, want node participant first at stable priority 1", ack)
		}
	}
	qs := queueOnce(t, home)
	if len(qs.Waiters) != 2 || qs.Waiters[0].RunID != "second-run" || qs.Waiters[0].Priority != 1 {
		t.Fatalf("waiters after priority: %+v", qs.Waiters)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	admitted := waitResult(t, result, 3*time.Second)
	if admitted.err != nil {
		t.Fatal(admitted.err)
	}
}
