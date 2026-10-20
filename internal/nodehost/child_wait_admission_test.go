package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const gib = int64(1) << 30

// budgetAdmitter is a machine with fixed cores and memory that refuses any
// request which does not fit beside the leases it holds.
type budgetAdmitter struct {
	mu   sync.Mutex
	free wingwire.HostResources
}

func (a *budgetAdmitter) Acquire(_ context.Context, request wingwire.AdmissionRequest, _ func(wingwire.Queued)) (Lease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if request.Resources.Cores > a.free.Cores || request.Resources.MemoryBytes > a.free.MemoryBytes {
		return nil, &wingdclient.AdmissionError{Policy: wingwire.PolicyFail, Key: "memory"}
	}
	a.free.Cores -= request.Resources.Cores
	a.free.MemoryBytes -= request.Resources.MemoryBytes
	return &budgetLease{admitter: a, resources: request.Resources, released: make(chan struct{})}, nil
}

type budgetLease struct {
	admitter  *budgetAdmitter
	resources wingwire.HostResources
	once      sync.Once
	released  chan struct{}
}

func (l *budgetLease) Token() string                     { return "token" }
func (l *budgetLease) Resources() wingwire.HostResources { return l.resources }
func (l *budgetLease) Release() error {
	l.once.Do(func() {
		l.admitter.mu.Lock()
		l.admitter.free.Cores += l.resources.Cores
		l.admitter.free.MemoryBytes += l.resources.MemoryBytes
		l.admitter.mu.Unlock()
		close(l.released)
	})
	return nil
}

func (l *budgetLease) Watch(func(wingwire.Evicted), func(wingwire.Cancel)) error {
	<-l.released
	return nil
}

func sizedJob(id string, cores float64, memory int64) store.NodeRow {
	row := localAdmissionRow(id)
	row.Execution.Resources.Value = runspec.Resources{}
	row.Execution.Resources.Value.Cores.Value = cores
	row.Execution.Resources.Value.MemoryBytes.Value = memory
	return row
}

// A job waiting on a child run still holds its process and heap, so its whole
// reservation stays taken: a child that fits beside it is admitted, and work
// that needs the waiting job's memory is refused.
func TestJobWaitingOnChildKeepsItsReservation(t *testing.T) {
	machine := wingwire.HostResources{Cores: 2, MemoryBytes: 2 * gib}
	admitter := &budgetAdmitter{free: machine}
	admission := &LocalAdmission{Daemon: admitter, BoxID: testBox}
	accepted := localAdmissionSeal(t, runspec.Options{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var hogErr, childErr error
	childRan := false
	input, persisted, host, _ := claimedNodeFixture(t, func(input *NodeInput, deps *ExecutorDependencies) {
		input.Execution.Resources.Value.Cores.Value = 1
		input.Execution.Resources.Value.MemoryBytes.Value = gib
		var spec runspec.Spec
		if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		spec.Resolved.Settings.Jobs[input.NodeID] = input.Execution
		input.Accepted.Spec, _ = json.Marshal(spec)
		start := deps.Start
		deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
			process, err := start(ctx, in, activity)
			process.(*executorProcess).onRun = func(context.Context) {
				defer activity.Await()()
				hogErr = admission.ClaimedNode(ctx, accepted, sizedJob("hog", 0, gib+gib/2), func(context.Context) error { return nil })
				childErr = admission.ClaimedNode(ctx, accepted, sizedJob("child", 1, gib/2), func(context.Context) error {
					childRan = true
					return nil
				})
			}
			return process, err
		}
	})
	persisted.row.Execution = input.Execution
	host.Admission = admission
	if err := host.Execute(ctx, input); err != nil {
		t.Fatal(err)
	}
	var refused *wingdclient.AdmissionError
	if !errors.As(hogErr, &refused) {
		t.Fatalf("a claim on the waiting job's memory was not refused: %v", hogErr)
	}
	if !childRan || childErr != nil {
		t.Fatalf("a child that fits beside its waiting parent was not admitted: %v", childErr)
	}
	if admitter.free != machine {
		t.Fatalf("leases leaked: %+v free", admitter.free)
	}
}
