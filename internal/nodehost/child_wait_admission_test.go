package nodehost

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// oneSlotAdmitter grants one lease at a time, like a machine with capacity for one job.
type oneSlotAdmitter struct {
	slot chan struct{}
	mu   sync.Mutex
	reqs []wingwire.AdmissionRequest
}

func (a *oneSlotAdmitter) Acquire(ctx context.Context, request wingwire.AdmissionRequest, _ func(wingwire.Queued)) (Lease, error) {
	a.mu.Lock()
	a.reqs = append(a.reqs, request)
	a.mu.Unlock()
	select {
	case a.slot <- struct{}{}:
		return &oneSlotLease{admitter: a, released: make(chan struct{})}, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

type oneSlotLease struct {
	admitter *oneSlotAdmitter
	once     sync.Once
	released chan struct{}
}

func (l *oneSlotLease) Token() string                     { return "token" }
func (l *oneSlotLease) Resources() wingwire.HostResources { return wingwire.HostResources{Cores: 1} }
func (l *oneSlotLease) Release() error {
	l.once.Do(func() {
		<-l.admitter.slot
		close(l.released)
	})
	return nil
}
func (l *oneSlotLease) Watch(func(wingwire.Evicted), func(wingwire.Cancel)) error {
	<-l.released
	return nil
}

func TestJobWaitingOnChildHandsBackItsOnlySlot(t *testing.T) {
	admitter := &oneSlotAdmitter{slot: make(chan struct{}, 1)}
	admission := &LocalAdmission{Daemon: admitter, BoxID: testBox}
	parent := wingwire.AdmissionRequest{RunID: wingwire.NodeHostParticipant("parent", "await"), Resources: wingwire.HostResources{Cores: 1}}
	child := wingwire.AdmissionRequest{RunID: wingwire.NodeHostParticipant("child", "stamp"), Resources: wingwire.HostResources{Cores: 1}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	childRan := false
	err := admission.hold(ctx, store.NodeRef{RunID: "parent", NodeID: "await"}, parent, true, func(ctx context.Context, _ Lease) error {
		resume := awaitChildAdmission(ctx)
		if err := admission.hold(ctx, store.NodeRef{RunID: "child", NodeID: "stamp"}, child, true, func(context.Context, Lease) error {
			childRan = true
			return nil
		}); err != nil {
			return err
		}
		resume()
		if len(admitter.slot) != 1 {
			t.Error("parent resumed without taking its slot back")
		}
		return context.Cause(ctx)
	})
	if err != nil || !childRan {
		t.Fatalf("parent and child did not both finish on one slot: child=%v %v", childRan, err)
	}
	if len(admitter.slot) != 0 {
		t.Fatal("a finished job kept the slot")
	}
	if last := admitter.reqs[len(admitter.reqs)-1]; last.RunID != parent.RunID || last.Priority != resumePriority {
		t.Fatalf("parent did not take its slot back ahead of new work: %+v", last)
	}
}

func TestJobInAConcurrencyGroupKeepsItsLeaseWhileWaiting(t *testing.T) {
	admitter := &oneSlotAdmitter{slot: make(chan struct{}, 1)}
	admission := &LocalAdmission{Daemon: admitter, BoxID: testBox}
	request := wingwire.AdmissionRequest{RunID: wingwire.NodeHostParticipant("parent", "deploy"), Semaphores: []wingwire.SemaphoreClaim{{Name: "deploy", Capacity: 1}}}
	err := admission.hold(t.Context(), store.NodeRef{RunID: "parent", NodeID: "deploy"}, request, false, func(ctx context.Context, _ Lease) error {
		resume := awaitChildAdmission(ctx)
		defer resume()
		if len(admitter.slot) != 1 {
			t.Error("a job released its concurrency group while waiting on a child")
		}
		return nil
	})
	if err != nil || len(admitter.reqs) != 1 {
		t.Fatalf("grouped job reacquired admission: %d %v", len(admitter.reqs), err)
	}
}
