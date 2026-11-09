package nodeproto

import (
	"errors"
	"sync"
	"testing"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestTracker(t *testing.T) {
	var tr Tracker
	a, b := tr.Start(nodewire.OpPlan), tr.Start(nodewire.OpEval)
	if a == b {
		t.Fatalf("ids repeat: %s", a)
	}
	if op, err := tr.Resolve(nodewire.Reply{ID: b}); err != nil || op != nodewire.OpEval {
		t.Fatalf("resolve b: %s %v", op, err)
	}
	if _, err := tr.Resolve(nodewire.Reply{ID: b}); !errors.Is(err, ErrDuplicateReply) {
		t.Fatalf("second reply: %v", err)
	}
	if _, err := tr.Resolve(nodewire.Reply{ID: "99"}); !errors.Is(err, ErrUnknownReply) {
		t.Fatalf("unknown reply: %v", err)
	}
	if p := tr.Pending(); len(p) != 1 || p[a] != nodewire.OpPlan {
		t.Fatalf("pending %v", p)
	}
}

func TestTrackerParallelSteps(t *testing.T) {
	var tr Tracker
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = tr.Start(nodewire.OpRunStep)
	}
	var wg sync.WaitGroup
	for i := len(ids) - 1; i >= 0; i-- {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if op, err := tr.Resolve(nodewire.Reply{ID: ids[i]}); err != nil || op != nodewire.OpRunStep {
				t.Errorf("resolve %s: %s %v", ids[i], op, err)
			}
		}()
	}
	wg.Wait()
	if p := tr.Pending(); len(p) != 0 {
		t.Fatalf("pending after all replies: %v", p)
	}
}
