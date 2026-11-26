package nodehost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type rateLimitedHost struct {
	PrivateHostState
	mu        sync.Mutex
	refusals  int
	after     time.Duration
	hang      bool
	giveUp    time.Time
	calls     []time.Time
	committed []runstate.NodeCommandPayload
}

func (h *rateLimitedHost) TransitionNode(ctx context.Context, _ runspec.NodeRef, _ runstate.Actor, payload runstate.NodeCommandPayload) (runstate.Applied, error) {
	h.mu.Lock()
	h.calls = append(h.calls, time.Now())
	if !h.giveUp.IsZero() && time.Now().After(h.giveUp) {
		h.mu.Unlock()
		return runstate.Applied{}, errors.New("still retrying past the test's deadline")
	}
	refuse := h.refusals != 0
	if refuse {
		h.refusals--
	}
	hang := !refuse && h.hang
	if !refuse && !hang {
		h.committed = append(h.committed, payload)
	}
	h.mu.Unlock()
	if hang {
		<-ctx.Done()
		return runstate.Applied{}, ctx.Err()
	}
	if refuse {
		return runstate.Applied{}, &apierr.Error{Code: hosterr.RateLimited, Message: "team request budget exhausted", Details: map[string]any{"retry_after_ms": h.after.Milliseconds()}}
	}
	return runstate.Applied{}, nil
}

func (h *rateLimitedHost) record() ([]time.Time, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Time(nil), h.calls...), len(h.committed)
}

func rateLimitedReporter(t *testing.T, host *rateLimitedHost) (*nodeExecutor, NodeInput) {
	t.Helper()
	input, _, deps, _ := executorFixture(t)
	state, err := NewRemoteHostStore(struct{ controllerwire.Controller }{}, host, "unit", func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	deps.Store = state
	executor, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	return executor.(*nodeExecutor), input
}

func TestReportOutlastsRateLimitedTransitions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := &rateLimitedHost{refusals: 5, after: 2 * time.Second}
		h, input := rateLimitedReporter(t, host)
		if err := h.report(t.Context(), input, runstate.NodeResult{Outcome: runstate.NodeSuccess}, nil); err != nil {
			t.Fatalf("report after five rate-limited refusals: %v", err)
		}
		if calls, committed := host.record(); len(calls) != 6 || committed != 1 {
			t.Fatalf("attempts=%d committed=%d, want the result recorded on the sixth", len(calls), committed)
		}
		accepted := host.committed[0]
		if accepted.Cause != runstate.CauseReport || accepted.To != runstate.NodeSuccess || accepted.Extra.Result == nil || accepted.Extra.Result.Outcome != runstate.NodeSuccess {
			t.Fatalf("accepted transition = %+v, want the successful report the refusals delayed", accepted)
		}
	})
}

func TestReportStopsRetryingWhenTheLeaseEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := &rateLimitedHost{refusals: -1, after: time.Second}
		h, input := rateLimitedReporter(t, host)
		const leaseLength = 30 * time.Second
		keeper := lease.Keeper{Lease: leaseLength, Interval: 10 * time.Second, Renew: func(context.Context) (lease.Beat, error) {
			return lease.Beat{}, errors.New("controller unreachable")
		}}
		unit, stop := keeper.Run(t.Context())
		defer stop()
		start := time.Now()
		err := h.report(unit, input, runstate.NodeResult{Outcome: runstate.NodeSuccess}, nil)
		calls, _ := host.record()
		if !errors.Is(err, lease.ErrSilent) || time.Since(start) > leaseLength+5*time.Second || len(calls) < 2 {
			t.Fatalf("report = %v after %d attempts in %v, want the lease's silent cause", err, len(calls), time.Since(start))
		}
	})
}

func TestReportUnderAPlainContextStopsWithinOneLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		host := &rateLimitedHost{refusals: -1, after: 100 * time.Second, giveUp: start.Add(2 * limits.DefaultLease)}
		h, input := rateLimitedReporter(t, host)
		err := h.report(context.Background(), input, runstate.NodeResult{Outcome: runstate.NodeSuccess}, nil)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != hosterr.RateLimited {
			t.Fatalf("report = %v, want the last rate-limited refusal", err)
		}
		calls, _ := host.record()
		if elapsed := time.Since(start); elapsed > limits.DefaultLease || len(calls) != 2 {
			t.Fatalf("report sent %d attempts over %v; a 100s Retry-After past the %v bound must end the retries", len(calls), elapsed, limits.DefaultLease)
		}
	})
}

func TestRateLimitedTransitionReturnsByTheLeaseDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := &rateLimitedHost{refusals: 1, after: 170 * time.Second, hang: true}
		h, input := rateLimitedReporter(t, host)
		start := time.Now()
		err := h.report(context.Background(), input, runstate.NodeResult{Outcome: runstate.NodeSuccess}, nil)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > limits.DefaultLease {
			t.Fatalf("report = %v after %v, want the request cut at the %v deadline", err, time.Since(start), limits.DefaultLease)
		}
	})
}

func TestRateLimitedTransitionsSpreadTheirRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const advised = 5 * time.Second
		var wg sync.WaitGroup
		hosts := []*rateLimitedHost{{refusals: 1, after: advised}, {refusals: 1, after: advised}}
		start := time.Now()
		for i, host := range hosts {
			h, input := rateLimitedReporter(t, host)
			wg.Go(func() {
				if err := h.report(t.Context(), input, runstate.NodeResult{Outcome: runstate.NodeSuccess}, nil); err != nil {
					t.Errorf("report %d: %v", i, err)
				}
			})
		}
		wg.Wait()
		first, _ := hosts[0].record()
		second, _ := hosts[1].record()
		if len(first) != 2 || len(second) != 2 {
			t.Fatalf("attempts = %d and %d, want one retry each", len(first), len(second))
		}
		if first[1].Equal(second[1]) || first[1].Sub(start) < advised+400*time.Millisecond || second[1].Sub(start) < advised+400*time.Millisecond {
			t.Fatalf("retries at %v and %v after a shared %v Retry-After", first[1].Sub(start), second[1].Sub(start), advised)
		}
	})
}
