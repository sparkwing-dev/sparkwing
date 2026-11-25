package client

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestJobFitsWaitsOnlyWhenTheChargeCannotFit(t *testing.T) {
	twoCores := func(held, available float64) []wingwire.ResourceState {
		return []wingwire.ResourceState{
			{Key: "cores", Capacity: 2, Held: held, Reserved: 0.4, Available: available},
			{Key: "memory", Capacity: 1 << 30, Available: 1 << 30},
		}
	}
	budget := &wingwire.BudgetState{Cores: 1.5, MachineCores: 16}
	for _, tc := range []struct {
		name  string
		state wingwire.QueueState
		cores float64
		want  bool
	}{
		{"a fraction of a core left refuses a one-core job", wingwire.QueueState{Resources: twoCores(1, 0.6)}, 1, false},
		{"an undeclared job is charged one core", wingwire.QueueState{Resources: twoCores(1, 0.6)}, 0, false},
		{"a fraction of a core left admits a job that fits it", wingwire.QueueState{Resources: twoCores(1, 0.6)}, 0.5, true},
		{"exactly the charge left fits", wingwire.QueueState{Resources: twoCores(0.6, 1)}, 1, true},
		{"an idle machine fits an oversized job", wingwire.QueueState{Resources: twoCores(0, 1.6)}, 4, true},
		{"a budget below the machine reserve still refuses a fraction of a core", wingwire.QueueState{Budget: budget, Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 1.5, Held: 1, Reserved: 3.2, Available: 0.5}}}, 1, false},
		{"an oversized job is judged at the room the ledger has", wingwire.QueueState{Budget: budget, Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 1.5, Reserved: 3.2, Available: 1.5}, {Key: "db", Capacity: 1, Held: 1}}}, 4, true},
		{"a request is rounded to whole millicores as the ledger rounds it", wingwire.QueueState{Resources: twoCores(1.1, 0.5)}, 0.5004, true},
		{"a request just over the free millicores still waits", wingwire.QueueState{Resources: twoCores(1.1, 0.5)}, 0.5006, false},
		{"room above the machine's grantable cores, as during a capacity refresh, leaves the clamp unknown", wingwire.QueueState{Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 8, Held: 1, Reserved: 3.2, Available: 7}}}, 7.5, true},
		{"a semaphore named cores is not the host CPU row", wingwire.QueueState{Resources: append(twoCores(0.5, 1.1), wingwire.ResourceState{Key: "cores", Capacity: 1, Held: 1})}, 1, true},
		{"a busy machine refuses an oversized job", wingwire.QueueState{Resources: twoCores(0.4, 1.2)}, 4, false},
		{"a waiter on cores cannot prove a smaller job will not backfill", wingwire.QueueState{Resources: twoCores(0.5, 1.1), Waiters: []wingwire.Waiter{{RunID: "ahead", WaitingOn: []string{"cores"}}}}, 1, true},
		{"no room at all refuses any charge", wingwire.QueueState{Resources: twoCores(1.6, 0)}, 0.1, false},
		{"admission off charges nothing", wingwire.QueueState{AdmissionMode: "off", Resources: twoCores(1.6, 0)}, 1, true},
		{"with no cores held the clamped charge cannot be bounded, so the job may fit", wingwire.QueueState{Resources: append(twoCores(0, 0.5), wingwire.ResourceState{Key: "db", Capacity: 1, Held: 1})}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := JobFits(tc.state, tc.cores); got != tc.want {
				t.Fatalf("JobFits(%+v, %v) = %t, want %t", tc.state, tc.cores, got, tc.want)
			}
		})
	}
}
